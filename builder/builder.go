package builder

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/hashicorp/waypoint-plugin-sdk/component"
	"github.com/hashicorp/waypoint-plugin-sdk/terminal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BuildConfig struct {
	Source     string `hcl:"source,optional"`
	OutputName string `hcl:"output_name,optional"`
	Dockerfile string `hcl:"dockerfile,optional"`
}

type Builder struct {
	config BuildConfig
}

// Implement Configurable
func (b *Builder) Config() (interface{}, error) {
	return &b.config, nil
}

// Implement ConfigurableNotify
func (b *Builder) ConfigSet(config interface{}) error {
	_, ok := config.(*BuildConfig)
	if !ok {
		// The Waypoint SDK should ensure this never gets hit
		return fmt.Errorf("expected *BuildConfig as parameter")
	}

	return nil
}

// Implement Builder
func (b *Builder) BuildFunc() interface{} {
	// return a function which will be called by Waypoint
	return b.build
}

// A BuildFunc does not have a strict signature, you can define the parameters
// you need based on the Available parameters that the Waypoint SDK provides.
// Waypoint will automatically inject parameters as specified
// in the signature at run time.
//
// Available input parameters:
// - context.Context
// - *component.Source
// - *component.JobInfo
// - *component.DeploymentConfig
// - hclog.Logger
// - terminal.UI
// - *component.LabelSet
//
// The output parameters for BuildFunc must be a Struct which can
// be serialzied to Protocol Buffers binary format and an error.
// This Output Value will be made available for other functions
// as an input parameter.
// If an error is returned, Waypoint stops the execution flow and
// returns an error to the user.
func (b *Builder) build(ctx context.Context, src *component.Source, ui terminal.UI) (*Zip, error) {
	sg := ui.StepGroup()
	defer sg.Wait()
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to create Docker client: %s", err)
	}

	dockerfile := b.config.Dockerfile

	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// Build image
	step := sg.Add("Building image...")
	defer step.Abort()

	stdout, _, err := ui.OutputWriters()
	if err != nil {
		return nil, err
	}

	imageTag := fmt.Sprintf("waypoint.local/%s", src.App)

	opts := types.ImageBuildOptions{
		Dockerfile: dockerfile,
		Tags:       []string{imageTag},
		Remove:     true,
	}

	buildCtx, err := archive.TarWithOptions(src.Path, &archive.TarOptions{})
	if err != nil {
		return nil, err
	}

	resp, err := dockerClient.ImageBuild(ctx, buildCtx, opts)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var termFd uintptr
	if f, ok := stdout.(*os.File); ok {
		termFd = f.Fd()
	}

	err = jsonmessage.DisplayJSONMessagesStream(resp.Body, step.TermOutput(), termFd, true, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to stream build logs to the terminal: %s", err)
	}

	step.Done()

	// Run container
	step = sg.Add("Running container...")
	defer step.Abort()

	containerResp, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image: imageTag,
		Cmd:   []string{"/bin/sh"},
		Tty:   false,
	}, nil, nil, nil, "")
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to create Docker container: %s", err)
	}

	step.Done()

	// Extract files from container
	step = sg.Add("Extracing assets...")
	defer step.Abort()

	content, stat, err := dockerClient.CopyFromContainer(ctx, containerResp.ID, b.config.Source)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to copy assets from Docker container: %s", err)
	}
	defer content.Close()

	srcInfo := archive.CopyInfo{
		Path:       b.config.Source,
		Exists:     true,
		IsDir:      stat.Mode.IsDir(),
		RebaseName: "", // TODO: Follow symbolic links
	}

	destDir, err := os.MkdirTemp("", "waypoint-plugin-s3")
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to create tmp directory: %s", err)
	}

	archive.CopyTo(content, srcInfo, destDir)

	step.Done()

	// Kill container
	step = sg.Add("Shutting down container...")
	defer step.Abort()

	dockerClient.ContainerRemove(ctx, containerResp.ID, types.ContainerRemoveOptions{Force: true})

	step.Done()

	// step = sg.Add("Zipping assets...")
	// defer step.Abort()

	// // TODO zip files

	// step.Done()

	return &Zip{
		Path: destDir,
	}, nil
}
