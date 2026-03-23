package main

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anthropics/edgessh/internal/config"
	"github.com/spf13/cobra"
)

const vmImageName = "edgessh-vm-rootfs"

// buildVMImage builds the default VM rootfs Docker image from Dockerfile.vm.
func buildVMImage() (string, error) {
	if err := buildDockerImage(vmImageName, "Dockerfile.vm", "  Building VM rootfs image..."); err != nil {
		return "", fmt.Errorf("docker build: %w", err)
	}
	return vmImageName, nil
}

func buildDockerImage(tag, dockerfileName, banner string) error {
	dockerfilePath, err := findDockerfile(dockerfileName)
	if err != nil {
		return err
	}
	fmt.Println(banner)
	build := exec.Command("docker", "build", "--platform", "linux/amd64", "-t", tag, "-f", dockerfilePath, filepath.Dir(dockerfilePath))
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	return build.Run()
}

func findDockerfile(name string) (string, error) {
	candidates := []string{name}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s not found — run from the edgessh repo root", name)
}

// createRootfsVolume exports a Docker image into a loophole volume.
func createRootfsVolume(cfg *config.Config, volumeName, image, size string) error {
	tmpDir, err := os.MkdirTemp("", "edgessh-rootfs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Export container filesystem
	fmt.Println("  Exporting Docker image filesystem...")
	var rnd [4]byte
	rand.Read(rnd[:])
	containerName := "edgessh-export-" + hex.EncodeToString(rnd[:])
	create := exec.Command("docker", "create", "--name", containerName, "--platform", "linux/amd64", image, "/bin/true")
	create.Stderr = os.Stderr
	if err := create.Run(); err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	defer exec.Command("docker", "rm", containerName).Run()

	tarPath := filepath.Join(tmpDir, "rootfs.tar")
	exportCmd := exec.Command("docker", "export", containerName)
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	exportCmd.Stdout = tarFile
	exportCmd.Stderr = os.Stderr
	if err := exportCmd.Run(); err != nil {
		tarFile.Close()
		return fmt.Errorf("docker export: %w", err)
	}
	tarFile.Close()

	initBin := "dist/edgessh-init"
	if _, err := os.Stat(initBin); err != nil {
		return fmt.Errorf("edgessh-init not found at %s — run 'make vminit' first", initBin)
	}
	poweroffBin := "dist/edgessh-poweroff"
	if _, err := os.Stat(poweroffBin); err != nil {
		return fmt.Errorf("edgessh-poweroff not found at %s — run 'make vmpoweroff' first", poweroffBin)
	}

	fmt.Println("  Appending root-owned helper files to tar...")
	if err := rewriteRootfsTar(tarPath, []tarExtraFile{
		{TarPath: "edgessh-init", SrcPath: initBin, Mode: 0o755},
		{TarPath: "edgessh-poweroff", SrcPath: poweroffBin, Mode: 0o755},
		{TarPath: "etc/resolv.conf", Content: []byte("nameserver 8.8.8.8\n"), Mode: 0o644},
	}); err != nil {
		return fmt.Errorf("rewrite rootfs tar: %w", err)
	}

	// Create loophole volume directly from the tarball via --mkfs.
	// The create command uploads blocks in parallel and creates an initial checkpoint.
	fmt.Println("  Uploading to loophole volume...")
	if err := runLoophole(cfg, "create", cfg.LoopholeStoreURL, volumeName, "--mkfs", tarPath, "--size", size); err != nil {
		return fmt.Errorf("loophole create: %w", err)
	}

	return nil
}

type tarExtraFile struct {
	TarPath string
	SrcPath string
	Content []byte
	Mode    int64
}

func rewriteRootfsTar(tarPath string, extras []tarExtraFile) error {
	src, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer src.Close()

	tmpPath := tarPath + ".tmp"
	dst, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)
	materializedTargets := map[string][]string{}
	skipEntries := map[string]bool{}
	for _, extra := range extras {
		skipEntries[extra.TarPath] = true
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			tw.Close()
			dst.Close()
			return err
		}
		if hdr.Typeflag == tar.TypeSymlink &&
			strings.HasPrefix(hdr.Name, "etc/ssl/certs/") &&
			strings.HasSuffix(hdr.Name, ".pem") &&
			strings.HasPrefix(hdr.Linkname, "/usr/share/ca-certificates/mozilla/") {
			target := strings.TrimPrefix(hdr.Linkname, "/")
			materializedTargets[target] = append(materializedTargets[target], hdr.Name)
			skipEntries[hdr.Name] = true
		}
	}

	if _, err := src.Seek(0, 0); err != nil {
		tw.Close()
		dst.Close()
		return err
	}
	tr = tar.NewReader(src)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			tw.Close()
			dst.Close()
			return err
		}
		if skipEntries[hdr.Name] {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			tw.Close()
			dst.Close()
			return err
		}
		var body bytes.Buffer
		reader := io.Reader(tr)
		if hdr.Typeflag == tar.TypeReg && len(materializedTargets[hdr.Name]) > 0 {
			reader = io.TeeReader(tr, &body)
		}
		if _, err := io.Copy(tw, reader); err != nil {
			tw.Close()
			dst.Close()
			return err
		}
		for _, alias := range materializedTargets[hdr.Name] {
			dupHdr := *hdr
			dupHdr.Name = alias
			dupHdr.Linkname = ""
			dupHdr.Typeflag = tar.TypeReg
			dupHdr.Size = int64(body.Len())
			dupHdr.Format = tar.FormatPAX
			if err := tw.WriteHeader(&dupHdr); err != nil {
				tw.Close()
				dst.Close()
				return err
			}
			if _, err := io.Copy(tw, bytes.NewReader(body.Bytes())); err != nil {
				tw.Close()
				dst.Close()
				return err
			}
		}
	}

	for _, extra := range extras {
		var body io.ReadCloser
		size := int64(len(extra.Content))
		if extra.SrcPath != "" {
			f, err := os.Open(extra.SrcPath)
			if err != nil {
				tw.Close()
				dst.Close()
				return err
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				tw.Close()
				dst.Close()
				return err
			}
			size = info.Size()
			body = f
		} else {
			body = io.NopCloser(io.LimitReader(io.MultiReader(), 0))
		}

		hdr := &tar.Header{
			Name:     extra.TarPath,
			Mode:     extra.Mode,
			Size:     size,
			Typeflag: tar.TypeReg,
			Uid:      0,
			Gid:      0,
			Uname:    "root",
			Gname:    "root",
		}
		if err := tw.WriteHeader(hdr); err != nil {
			if extra.SrcPath != "" {
				body.Close()
			}
			tw.Close()
			dst.Close()
			return err
		}
		if extra.SrcPath != "" {
			if _, err := io.Copy(tw, body); err != nil {
				body.Close()
				tw.Close()
				dst.Close()
				return err
			}
			body.Close()
		} else if _, err := tw.Write(extra.Content); err != nil {
			tw.Close()
			dst.Close()
			return err
		}
	}

	if err := tw.Close(); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, tarPath); err != nil {
		return err
	}
	return nil
}

func loopholeEnv(cfg *config.Config) []string {
	return append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.R2AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+cfg.R2SecretAccessKey,
		"AWS_REGION=auto",
	)
}

func loopholeCommand(cfg *config.Config, args ...string) *exec.Cmd {
	cmd := exec.Command("loophole", args...)
	cmd.Env = loopholeEnv(cfg)
	return cmd
}

func runLoophole(cfg *config.Config, args ...string) error {
	cmd := loopholeCommand(cfg, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func loopholeCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "loophole [args...]",
		Short:              "Run loophole with R2 credentials from edgessh config",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireSetup()
			if err != nil {
				return err
			}
			// Prepend store URL if first arg is a subcommand that needs it
			c := loopholeCommand(cfg, args...)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
