package parser

import (
	"strings"

	dtypes "github.com/docker/docker/api/types"

	"github.com/guumaster/hostctl/pkg/docker"
	"github.com/guumaster/hostctl/pkg/types"
)

// NewProfileFromDocker creates a new profile from docker info.
func NewProfileFromDocker(opts *docker.Options) (*types.Profile, error) {
	p := &types.Profile{}

	containers, err := docker.GetContainerList(opts)
	if err != nil {
		return nil, err
	}

	addFromContainer(p, containers, opts)

	return p, err
}

// NewProfileFromDockerCompose creates a new profile from docker info.
//
// Containers are attributed to compose services by joining three explicit
// data sources: the service manifest of the compose file, the selected
// project name and the running containers metadata (compose labels and
// container names). Attribution errors abort before any profile is returned
// so hosts content is never replaced on ambiguity.
func NewProfileFromDockerCompose(opts *docker.Options) (*types.Profile, error) {
	p := &types.Profile{}

	services, err := docker.ParseComposeFile(opts.ComposeFile)
	if err != nil {
		return nil, err
	}

	containers, err := docker.GetContainerList(opts)
	if err != nil {
		return nil, err
	}

	err = addFromComposeService(p, containers, services, opts)
	if err != nil {
		return nil, err
	}

	return p, nil
}

func addFromContainer(profile *types.Profile, containers []dtypes.Container, opts *docker.Options) {
	var routes []*types.Route

	for _, c := range containers {
		for _, n := range c.NetworkSettings.Networks {
			if opts.NetworkID != "" && n.NetworkID != opts.NetworkID {
				continue
			}

			name := strings.Replace(c.Names[0], "/", "", -1)

			name = name + "." + opts.Domain
			routes = append(routes, types.NewRoute(n.IPAddress, name))
		}
	}

	profile.AddRoutes(routes)
}

func addFromComposeService(
	profile *types.Profile,
	containers []dtypes.Container,
	services map[string]*docker.ComposeService,
	opts *docker.Options,
) error {
	var routes []*types.Route

	for _, c := range containers {
		attribution, err := docker.AttributeContainer(c, opts.ProjectName, services)
		if err != nil {
			return err
		}

		if attribution == nil || c.NetworkSettings == nil {
			continue
		}

		fullName := ""
		if len(c.Names) > 0 {
			fullName = strings.TrimPrefix(c.Names[0], "/")
		}

		for _, n := range c.NetworkSettings.Networks {
			if opts.NetworkID != "" && n.NetworkID != opts.NetworkID {
				continue
			}

			host := attribution.HostBase
			if opts.KeepPrefix && fullName != "" {
				host = fullName
			}

			routes = append(routes, types.NewRoute(n.IPAddress, host+"."+opts.Domain))
		}
	}

	profile.AddRoutes(routes)

	return nil
}
