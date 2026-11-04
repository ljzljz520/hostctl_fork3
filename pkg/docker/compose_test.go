package docker

import (
	"strings"
	"testing"

	dtypes "github.com/docker/docker/api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/guumaster/hostctl/pkg/types"
)

const composeFileContent = `
version: "3"
services:

  container1:
    image: some_image:3.5
    networks:
      - networkName1

  container2:
    image: some_other_image:1.0
    networks:
      - networkName1

  another-thing:
    container_name: db
    image: db:1.0
    networks:
      - networkName1

networks:
  networkName1:
`

func TestParseComposeFile(t *testing.T) {
	r := strings.NewReader(composeFileContent)
	services, err := ParseComposeFile(r)
	assert.NoError(t, err)

	assert.Len(t, services, 3)
	assert.Equal(t, &ComposeService{Name: "container1"}, services["container1"])
	assert.Equal(t, &ComposeService{Name: "container2"}, services["container2"])
	assert.Equal(t, &ComposeService{Name: "another-thing", ContainerName: "db"}, services["another-thing"])
}

func TestParseComposeFileDuplicateContainerName(t *testing.T) {
	r := strings.NewReader(`
version: "3"
services:
  web:
    container_name: shared
    image: nginx
  webcache:
    container_name: shared
    image: redis
`)

	_, err := ParseComposeFile(r)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrAmbiguousComposeContainer)
}

func TestNormalizeProjectName(t *testing.T) {
	assert.Equal(t, "testing-app", NormalizeProjectName("Testing-App"))
	assert.Equal(t, "my_project", NormalizeProjectName("My_Project!"))
	assert.Equal(t, "a_b-c", NormalizeProjectName("A_B-C"))
	assert.Equal(t, "abc", NormalizeProjectName("a b.c"))
}

func labelled(project, service string) map[string]string {
	return map[string]string{
		LabelProject: project,
		LabelService: service,
	}
}

func container(name string, labels map[string]string) dtypes.Container {
	return dtypes.Container{
		ID:     "id-" + name,
		Names:  []string{"/" + name},
		Labels: labels,
	}
}

func webServices() map[string]*ComposeService {
	return map[string]*ComposeService{
		"web":      {Name: "web"},
		"webcache": {Name: "webcache"},
		"db":       {Name: "db", ContainerName: "redis"},
	}
}

func TestAttributeContainerLabels(t *testing.T) {
	services := webServices()

	t.Run("v2 hyphen naming with replicas", func(t *testing.T) {
		m, err := AttributeContainer(container("app-web-1", labelled("app", "web")), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web", m.ServiceName)
		assert.Equal(t, "web-1", m.HostBase)
		assert.Equal(t, "-", string(m.Separator))
		assert.Equal(t, "1", m.Replica)

		m2, err := AttributeContainer(container("app-web-3", labelled("app", "web")), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m2)
		assert.Equal(t, "web-3", m2.HostBase)
	})

	t.Run("web does not steal webcache and vice versa", func(t *testing.T) {
		m, err := AttributeContainer(container("app-webcache-1", labelled("app", "webcache")), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "webcache", m.ServiceName)
		assert.Equal(t, "webcache-1", m.HostBase)
	})

	t.Run("similar project names never mix", func(t *testing.T) {
		// "myapp" and "appx" both contain the substring "app": labels decide.
		m, err := AttributeContainer(container("myapp-web-1", labelled("myapp", "web")), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)

		m, err = AttributeContainer(container("appx-web-1", labelled("appx", "web")), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)

		// even a v1 underscore name that the old substring check would catch
		m, err = AttributeContainer(container("myapp_web_1", labelled("myapp", "web")), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)
	})

	t.Run("service missing from the parsed file is skipped", func(t *testing.T) {
		m, err := AttributeContainer(container("app-worker-1", labelled("app", "worker")), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)
	})

	t.Run("custom container_name keeps its own name", func(t *testing.T) {
		m, err := AttributeContainer(container("redis", labelled("app", "db")), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "db", m.ServiceName)
		assert.Equal(t, "redis", m.HostBase)
	})

	t.Run("v1 underscore naming", func(t *testing.T) {
		m, err := AttributeContainer(container("app_web_2", labelled("app", "web")), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web_2", m.HostBase)
		assert.Equal(t, "_", string(m.Separator))
	})

	t.Run("hyphenated service keeps the service name intact", func(t *testing.T) {
		s := map[string]*ComposeService{"a-b": {Name: "a-b"}}
		m, err := AttributeContainer(container("p_a-b_1", labelled("p", "a-b")), "p", s)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "a-b_1", m.HostBase)
	})
}

func TestAttributeContainerFallback(t *testing.T) {
	services := webServices()

	t.Run("exact v1 match", func(t *testing.T) {
		m, err := AttributeContainer(container("app_web_1", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web", m.ServiceName)
		assert.Equal(t, "web_1", m.HostBase)
	})

	t.Run("exact v2 match", func(t *testing.T) {
		m, err := AttributeContainer(container("app-web-1", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web-1", m.HostBase)
	})

	t.Run("replicas resolve to different suffixes", func(t *testing.T) {
		m1, err := AttributeContainer(container("app-web-1", nil), "app", services)
		require.NoError(t, err)
		m2, err := AttributeContainer(container("app-web-2", nil), "app", services)
		require.NoError(t, err)
		assert.Equal(t, "web-1", m1.HostBase)
		assert.Equal(t, "web-2", m2.HostBase)
	})

	t.Run("web and webcache do not cross-match", func(t *testing.T) {
		m, err := AttributeContainer(container("app_webcache_1", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "webcache", m.ServiceName)

		m, err = AttributeContainer(container("app-webcache-1", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "webcache", m.ServiceName)
	})

	t.Run("similar project names are rejected without labels", func(t *testing.T) {
		for _, name := range []string{"myapp_web_1", "myapp-web-1", "appx_web_1", "app-web-extra-1"} {
			m, err := AttributeContainer(container(name, nil), "app", services)
			require.NoError(t, err)
			assert.Nil(t, m, name)
		}
	})

	t.Run("custom container_name exact match", func(t *testing.T) {
		m, err := AttributeContainer(container("redis", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "db", m.ServiceName)
		assert.Equal(t, "redis", m.HostBase)

		// a substring of a custom name is not enough
		m, err = AttributeContainer(container("redis2", nil), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)

		// prefixed names never satisfy a custom container_name
		m, err = AttributeContainer(container("app-redis-1", nil), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)
	})

	t.Run("one-off run containers", func(t *testing.T) {
		m, err := AttributeContainer(container("app_web_run_1", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web_run_1", m.HostBase)

		m, err = AttributeContainer(container("app-web-run-1", nil), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web-run-1", m.HostBase)
	})

	t.Run("mixed separators are not accepted", func(t *testing.T) {
		m, err := AttributeContainer(container("app_web-1", nil), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)
	})

	t.Run("ambiguous custom names reported as error", func(t *testing.T) {
		s := map[string]*ComposeService{
			"web": {Name: "web", ContainerName: "dup"},
			"db":  {Name: "db", ContainerName: "dup"},
		}
		_, err := AttributeContainer(container("dup", nil), "app", s)
		require.Error(t, err)
		assert.ErrorIs(t, err, types.ErrAmbiguousComposeContainer)
	})

	t.Run("project tag from another project forbids name fallback", func(t *testing.T) {
		m, err := AttributeContainer(container("app-web-1", labelled("other", "web")), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)
	})
}

func TestAttributeContainerServiceLabelWithoutProject(t *testing.T) {
	services := webServices()

	t.Run("name proves project membership", func(t *testing.T) {
		labels := map[string]string{LabelService: "web"}

		m, err := AttributeContainer(container("app-web-1", labels), "app", services)
		require.NoError(t, err)
		require.NotNil(t, m)
		assert.Equal(t, "web-1", m.HostBase)
	})

	t.Run("name from another project is rejected", func(t *testing.T) {
		labels := map[string]string{LabelService: "web"}

		m, err := AttributeContainer(container("myapp-web-1", labels), "app", services)
		require.NoError(t, err)
		assert.Nil(t, m)
	})
}
