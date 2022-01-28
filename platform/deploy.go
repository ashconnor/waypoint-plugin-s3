package platform

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/waypoint-plugin-s3/registry"
	"github.com/hashicorp/waypoint-plugin-sdk/component"
	"github.com/hashicorp/waypoint-plugin-sdk/terminal"
)

type DeployConfig struct {
	Region     string `hcl:"region,optional"`
	BucketName string `hcl:"bucket_name,optional"`
}

type Platform struct {
	config DeployConfig
}

// Implement Configurable
func (p *Platform) Config() (interface{}, error) {
	return &p.config, nil
}

// Implement ConfigurableNotify
func (p *Platform) ConfigSet(config interface{}) error {
	c, ok := config.(*DeployConfig)
	if !ok {
		// The Waypoint SDK should ensure this never gets hit
		return fmt.Errorf("expected *DeployConfig as parameter")
	}

	// validate the config
	if c.Region == "" {
		return fmt.Errorf("region must be set to a valid AWS region")
	}

	if c.BucketName == "" {
		return fmt.Errorf("bucket_name must be set to a valid S3 bucket")
	}

	return nil
}

// This function can be implemented to return various connection info required
// to connect to your given platform for Resource Manager. It could return
// a struct with client information, what namespace to connect to, a config,
// and so on.
func (p *Platform) getConnectContext() (interface{}, error) {
	return nil, nil
}

// Implement Platform
func (p *Platform) DeployFunc() interface{} {
	// return a function which will be called by Waypoint
	return p.deploy
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

// In addition to default input parameters the registry.Zip from the Build step
// can also be injected.
//
// The output parameters for BuildFunc must be a Struct which can
// be serialzied to Protocol Buffers binary format and an error.
// This Output Value will be made available for other functions
// as an input parameter.
// If an error is returned, Waypoint stops the execution flow and
// returns an error to the user.
func (b *Platform) deploy(
	ctx context.Context,
	ui terminal.UI,
	log hclog.Logger,
	dcr *component.DeclaredResourcesResp,
	zip *registry.Zip,
) (*Deployment, error) {
	u := ui.Status()
	defer u.Close()
	u.Update("Deploy application")
	// the session the S3 Uploader will use
	sess := session.Must(session.NewSession(&aws.Config{Region: &b.config.Region}))

	// create an uploader with the session and default options
	uploader := s3manager.NewUploader(sess)

	// walk temp dir
	objects := []s3manager.BatchUploadObject{}

	err := filepath.Walk(zip.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		stat, err := os.Stat(path)
		if err != nil {
			return err
		}

		if stat.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(zip.Path, path)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open file %q, %v", path, err)
		}

		fileInfo, _ := f.Stat()
		size := fileInfo.Size()

		buffer := make([]byte, size)
		f.Read(buffer)

		objects = append(objects, s3manager.BatchUploadObject{Object: &s3manager.UploadInput{
			Key:         aws.String(relativePath),
			Bucket:      aws.String(b.config.BucketName),
			Body:        bytes.NewReader(buffer),
			ACL:         aws.String("public-read"),
			ContentType: aws.String(http.DetectContentType(buffer)),
		}})

		return nil
	})

	if err != nil {
		return nil, err
	}

	iter := &s3manager.UploadObjectsIterator{Objects: objects}
	err = uploader.UploadWithIterator(ctx, iter)
	if err != nil {
		return nil, err
	}

	u.Update("Application deployed")

	return &Deployment{}, nil
}

func (b *Platform) resourceDeploymentCreate(
	ctx context.Context,
	log hclog.Logger,
	st terminal.Status,
	ui terminal.UI,
	zip *registry.Zip,
	result *Deployment,
) error {
	// Create your deployment resource here!

	return nil
}

func (b *Platform) resourceDeploymentStatus(
	ctx context.Context,
	ui terminal.UI,
	sg terminal.StepGroup,
	zip *registry.Zip,
) error {
	// Determine health status of "this" resource.
	return nil
}
