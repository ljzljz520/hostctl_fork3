package docker

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	dtypes "github.com/docker/docker/api/types"
	"gopkg.in/yaml.v2"

	"github.com/guumaster/hostctl/pkg/types"
)

// Labels docker compose attaches to every container it creates.
const (
	LabelProject      = "com.docker.compose.project"
	LabelService      = "com.docker.compose.service"
	LabelContainerNum = "com.docker.compose.container-number"
)

// ComposeData represents data in a docker-compose.yml file.
type composeData struct {
	Services map[string]composeService `yaml:"services"`
}

// composeService is the YAML representation of one compose service.
type composeService struct {
	ContainerName string `yaml:"container_name"`
}

// ComposeService represents one service from a yml file.
type ComposeService struct {
	// Name is the service key as declared in the compose file.
	Name string
	// ContainerName holds the optional custom container_name setting.
	ContainerName string
}

var projectNameInvalidChars = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizeProjectName mirrors docker compose project name normalization:
// the value is lowercased and every character that is not a letter, a digit,
// an underscore or a hyphen is removed.
func NormalizeProjectName(name string) string {
	return projectNameInvalidChars.ReplaceAllString(strings.ToLower(name), "")
}

// ParseComposeFile returns the services declared in a docker-compose.yml file,
// keyed by their service name.
func ParseComposeFile(r io.Reader) (map[string]*ComposeService, error) {
	bytes, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	data := &composeData{}

	err = yaml.Unmarshal(bytes, &data)
	if err != nil {
		return nil, fmt.Errorf("error parsing docker-compose content: %w", err)
	}

	services := make(map[string]*ComposeService, len(data.Services))
	customNames := map[string]string{}

	// iterate in a deterministic order so validation errors stay stable
	serviceNames := make([]string, 0, len(data.Services))

	for name := range data.Services {
		serviceNames = append(serviceNames, name)
	}

	sort.Strings(serviceNames)

	for _, name := range serviceNames {
		svc := data.Services[name]
		services[name] = &ComposeService{
			Name:          name,
			ContainerName: svc.ContainerName,
		}

		if svc.ContainerName == "" {
			continue
		}

		// Two services forcing the same container_name can never be told apart
		// from container metadata: reject the manifest before touching hosts.
		if other, ok := customNames[svc.ContainerName]; ok {
			return nil, fmt.Errorf(
				"%w: services %q and %q both declare container_name %q",
				types.ErrAmbiguousComposeContainer, other, name, svc.ContainerName,
			)
		}

		customNames[svc.ContainerName] = name
	}

	return services, nil
}

// ContainerAttribution is the result of proving that a running container
// implements one service of the selected compose project.
type ContainerAttribution struct {
	// ServiceName is the manifest service the container belongs to.
	ServiceName string
	// HostBase is the hostname part before the domain: either the custom
	// container name or the service name followed by its replica suffix.
	HostBase string
	// Replica holds the numeric (or one-off "run_<n>") suffix, when present.
	Replica string
	// Separator is the '_' (compose v1) or '-' (compose v2) separator used
	// in the container name.
	Separator byte
}

// AttributeContainer decides, without guessing, whether a running container
// belongs to the given compose project and which service it implements.
//
// Docker compose labels are authoritative: project and service tags are
// checked first so that similarly named projects (e.g. "app" vs "myapp")
// and similarly named services (e.g. "web" vs "webcache") never mix.
//
// Containers without compose labels are only accepted through an exact,
// anchored compatibility match against either the service container_name or
// the default "<project>_<service>_<n>" / "<project>-<service>-<n>" naming.
// If more than one service could own the same container an error is returned
// so callers can abort before replacing the hosts profile.
//
// A nil attribution with no error means the container provably does not
// belong to the selected project.
func AttributeContainer(
	c dtypes.Container,
	project string,
	services map[string]*ComposeService,
) (*ContainerAttribution, error) {
	project = NormalizeProjectName(project)
	name := containerName(c)

	if project == "" || name == "" {
		return nil, nil
	}

	projectLabel := c.Labels[LabelProject]
	serviceLabel := c.Labels[LabelService]

	switch {
	// Explicitly tagged as belonging to a different compose project.
	case projectLabel != "" && projectLabel != project:
		return nil, nil

	// Project confirmed by labels. A missing/unknown service label still
	// goes through the exact name match, but project provenance is known.
	case projectLabel == project:
		if serviceLabel == "" {
			return attributeByName(name, project, services)
		}

		svc, ok := services[serviceLabel]
		if !ok {
			// Part of this project but not declared in the parsed file.
			return nil, nil
		}

		return labelledAttribution(name, project, svc, c.Labels[LabelContainerNum]), nil

	// Service known but project unknown: the name itself must prove that the
	// container belongs to the selected project.
	case serviceLabel != "":
		svc, ok := services[serviceLabel]
		if !ok {
			return nil, nil
		}

		return exactServiceMatch(name, project, svc), nil

	// No compose metadata at all: only provable exact matches are allowed.
	default:
		return attributeByName(name, project, services)
	}
}

// labelledAttribution builds the attribution for a container whose project
// and service membership are proven by compose labels.
func labelledAttribution(name, project string, svc *ComposeService, numLabel string) *ContainerAttribution {
	if svc.ContainerName != "" && name == svc.ContainerName {
		return &ContainerAttribution{ServiceName: svc.Name, HostBase: name}
	}

	if sep, suffix, ok := matchDefaultName(name, project, svc.Name); ok {
		return &ContainerAttribution{
			ServiceName: svc.Name,
			HostBase:    svc.Name + string(sep) + suffix,
			Replica:     suffix,
			Separator:   sep,
		}
	}

	// Container was renamed (or is a one-off whose name does not follow the
	// default pattern): keep it addressable through its service name, using
	// the compose container-number tag when available.
	base := svc.Name
	if isDigits(numLabel) {
		base = svc.Name + "-" + numLabel
	}

	return &ContainerAttribution{ServiceName: svc.Name, HostBase: base, Separator: '-'}
}

// exactServiceMatch accepts a service-tagged-but-not-project-tagged container
// only when its name is an exact container_name or default-name match.
func exactServiceMatch(name, project string, svc *ComposeService) *ContainerAttribution {
	if svc.ContainerName != "" && name == svc.ContainerName {
		return &ContainerAttribution{ServiceName: svc.Name, HostBase: name}
	}

	if sep, suffix, ok := matchDefaultName(name, project, svc.Name); ok {
		return &ContainerAttribution{
			ServiceName: svc.Name,
			HostBase:    svc.Name + string(sep) + suffix,
			Replica:     suffix,
			Separator:   sep,
		}
	}

	return nil
}

type nameCandidate struct {
	service string
	sep     byte
	suffix  string
	custom  bool
}

// attributeByName scans the manifest for services whose container_name or
// default naming exactly matches the container name. Zero matches mean the
// container is not part of the project; more than one is ambiguous.
func attributeByName(
	name, project string,
	services map[string]*ComposeService,
) (*ContainerAttribution, error) {
	candidates := []nameCandidate{}
	seen := map[string]bool{}

	serviceNames := make([]string, 0, len(services))

	for n := range services {
		serviceNames = append(serviceNames, n)
	}

	sort.Strings(serviceNames)

	for _, sn := range serviceNames {
		svc := services[sn]

		if svc.ContainerName != "" && name == svc.ContainerName {
			if !seen[svc.Name] {
				candidates = append(candidates, nameCandidate{service: svc.Name, custom: true})
				seen[svc.Name] = true
			}
		}

		if sep, suffix, ok := matchDefaultName(name, project, svc.Name); ok && !seen[svc.Name] {
			candidates = append(candidates, nameCandidate{
				service: svc.Name,
				sep:     sep,
				suffix:  suffix,
			})
			seen[svc.Name] = true
		}
	}

	switch len(candidates) {
	case 0:
		return nil, nil
	case 1:
		c := candidates[0]

		base := name
		if !c.custom {
			base = c.service + string(c.sep) + c.suffix
		}

		return &ContainerAttribution{
			ServiceName: c.service,
			HostBase:    base,
			Replica:     c.suffix,
			Separator:   c.sep,
		}, nil
	default:
		names := make([]string, 0, len(candidates))
		for _, c := range candidates {
			names = append(names, c.service)
		}

		return nil, fmt.Errorf(
			"%w: container %q could belong to services %s of compose project %q; "+
				"recreate it with docker compose labels or use a unique container_name",
			types.ErrAmbiguousComposeContainer, name, strings.Join(names, ", "), project,
		)
	}
}

// matchDefaultName reports whether name is exactly the default compose
// container name for the service: project<sep>service<sep><replica>.
// Both the underscore separator used by docker-compose v1 and the hyphen
// separator used by docker compose v2 are accepted, and the replica is a
// plain number ("1") or a one-off run suffix ("run_1" / "run-1").
func matchDefaultName(name, project, service string) (sep byte, suffix string, ok bool) {
	for _, s := range []byte{'_', '-'} {
		prefix := project + string(s) + service + string(s)
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		rest := name[len(prefix):]

		if isDigits(rest) {
			return s, rest, true
		}

		runPrefix := "run" + string(s)
		if strings.HasPrefix(rest, runPrefix) && isDigits(rest[len(runPrefix):]) {
			return s, rest, true
		}
	}

	return 0, "", false
}

func containerName(c dtypes.Container) string {
	if len(c.Names) == 0 {
		return ""
	}

	return strings.TrimPrefix(c.Names[0], "/")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
