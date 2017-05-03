package controller

import (
	"fmt"
	"testing"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	clientgotesting "k8s.io/client-go/testing"
	kapi "k8s.io/kubernetes/pkg/api"

	client "github.com/openshift/origin/pkg/client/testclient"
	"github.com/openshift/origin/pkg/dockerregistry"
	"github.com/openshift/origin/pkg/image/api"

	_ "github.com/openshift/origin/pkg/api/install"
)

type expectedImage struct {
	Tag   string
	ID    string
	Image *dockerregistry.Image
	Err   error
}

type fakeDockerRegistryClient struct {
	Registry                 string
	Namespace, Name, Tag, ID string
	Insecure                 bool

	Tags    map[string]string
	Err     error
	ConnErr error

	Images []expectedImage

	Called bool
}

func (f *fakeDockerRegistryClient) Connect(registry string, insecure bool) (dockerregistry.Connection, error) {
	f.Called = true
	f.Registry = registry
	f.Insecure = insecure
	return f, f.ConnErr
}

func (f *fakeDockerRegistryClient) ImageTags(namespace, name string) (map[string]string, error) {
	f.Called = true
	f.Namespace, f.Name = namespace, name
	return f.Tags, f.Err
}

func (f *fakeDockerRegistryClient) ImageByTag(namespace, name, tag string) (*dockerregistry.Image, error) {
	f.Called = true
	if len(tag) == 0 {
		tag = api.DefaultImageTag
	}
	f.Namespace, f.Name, f.Tag = namespace, name, tag
	for _, t := range f.Images {
		if t.Tag == tag {
			return t.Image, t.Err
		}
	}
	return nil, dockerregistry.NewImageNotFoundError(fmt.Sprintf("%s/%s", namespace, name), tag, tag)
}

func (f *fakeDockerRegistryClient) ImageManifest(namespace, name, tag string) (string, []byte, error) {
	return "", nil, dockerregistry.NewImageNotFoundError(fmt.Sprintf("%s/%s", namespace, name), tag, tag)
}

func (f *fakeDockerRegistryClient) ImageByID(namespace, name, id string) (*dockerregistry.Image, error) {
	f.Called = true
	f.Namespace, f.Name, f.ID = namespace, name, id
	for _, t := range f.Images {
		if t.ID == id {
			return t.Image, t.Err
		}
	}
	return nil, dockerregistry.NewImageNotFoundError(fmt.Sprintf("%s/%s", namespace, name), id, "")
}

func TestControllerStart(t *testing.T) {
	two := int64(2)
	testCases := []struct {
		stream *api.ImageStream
		run    bool
	}{
		{
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: metav1.Now().UTC().Format(time.RFC3339)},
					Name:        "test",
					Namespace:   "other",
				},
			},
		},
		{
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: metav1.Now().UTC().Format(time.RFC3339)},
					Name:        "test",
					Namespace:   "other",
				},
				Spec: api.ImageStreamSpec{
					DockerImageRepository: "test/other",
				},
			},
		},
		{
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: "a random error"},
					Name:        "test",
					Namespace:   "other",
				},
				Spec: api.ImageStreamSpec{
					DockerImageRepository: "test/other",
				},
			},
		},

		// references are ignored
		{
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "other"},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:      &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
							Reference: true,
						},
					},
				},
			},
		},
		{
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "other"},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:      &kapi.ObjectReference{Kind: "AnotherImage", Name: "test/other:latest"},
							Reference: true,
						},
					},
				},
			},
		},

		// spec tag will be imported
		{
			run: true,
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "other"},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From: &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
						},
					},
				},
			},
		},
		// spec tag with generation with no pending status will be imported
		{
			run: true,
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "other"},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:       &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
							Generation: &two,
						},
					},
				},
			},
		},
		// spec tag with generation with older status generation will be imported
		{
			run: true,
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "other"},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:       &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
							Generation: &two,
						},
					},
				},
				Status: api.ImageStreamStatus{
					Tags: map[string]api.TagEventList{"latest": {Items: []api.TagEvent{{Generation: 1}}}},
				},
			},
		},
		// spec tag with generation with status condition error and equal generation will not be imported
		{
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: metav1.Now().UTC().Format(time.RFC3339)},
					Name:        "test",
					Namespace:   "other",
				},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:       &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
							Generation: &two,
						},
					},
				},
				Status: api.ImageStreamStatus{
					Tags: map[string]api.TagEventList{"latest": {Conditions: []api.TagEventCondition{
						{
							Type:       api.ImportSuccess,
							Status:     kapi.ConditionFalse,
							Generation: 2,
						},
					}}},
				},
			},
		},
		// spec tag with generation with status condition error and older generation will be imported
		{
			run: true,
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: metav1.Now().UTC().Format(time.RFC3339)},
					Name:        "test",
					Namespace:   "other",
				},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:       &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
							Generation: &two,
						},
					},
				},
				Status: api.ImageStreamStatus{
					Tags: map[string]api.TagEventList{"latest": {Conditions: []api.TagEventCondition{
						{
							Type:       api.ImportSuccess,
							Status:     kapi.ConditionFalse,
							Generation: 1,
						},
					}}},
				},
			},
		},
		// spec tag with generation with older status generation will be imported
		{
			run: true,
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: metav1.Now().UTC().Format(time.RFC3339)},
					Name:        "test",
					Namespace:   "other",
				},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"latest": {
							From:       &kapi.ObjectReference{Kind: "DockerImage", Name: "test/other:latest"},
							Generation: &two,
						},
					},
				},
				Status: api.ImageStreamStatus{
					Tags: map[string]api.TagEventList{"latest": {Items: []api.TagEvent{{Generation: 1}}}},
				},
			},
		},
		// test external repo
		{
			run: true,
			stream: &api.ImageStream{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "other",
				},
				Spec: api.ImageStreamSpec{
					Tags: map[string]api.TagReference{
						"1.1": {
							From: &kapi.ObjectReference{
								Kind: "DockerImage",
								Name: "some/repo:mytag",
							},
						},
					},
				},
			},
		},
	}

	for i, test := range testCases {
		fake := &client.Fake{}
		c := ImportController{streams: fake}
		other, err := kapi.Scheme.DeepCopy(test.stream)
		if err != nil {
			t.Fatal(err)
		}

		if err := c.Next(test.stream, nil); err != nil {
			t.Errorf("%d: unexpected error: %v", i, err)
		}
		if test.run {
			actions := fake.Actions()
			if len(actions) == 0 {
				t.Errorf("%d: expected remote calls: %#v", i, fake)
				continue
			}
			if !actions[0].Matches("create", "imagestreamimports") {
				t.Errorf("expected a create action: %#v", actions)
			}
		} else {
			if !kapi.Semantic.DeepEqual(test.stream, other) {
				t.Errorf("%d: did not expect change to stream: %s", i, diff.ObjectGoPrintDiff(test.stream, other))
			}
			if len(fake.Actions()) != 0 {
				t.Errorf("%d: did not expect remote calls", i)
			}
		}
	}
}

func TestScheduledImport(t *testing.T) {
	fake := &client.Fake{}
	b := newScheduled(true, fake, 1, nil, nil)

	one := int64(1)
	stream := &api.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "other", UID: "1", ResourceVersion: "1",
			Annotations: map[string]string{api.DockerImageRepositoryCheckAnnotation: "done"},
			Generation:  1,
		},
		Spec: api.ImageStreamSpec{
			Tags: map[string]api.TagReference{
				"default": {
					From:         &kapi.ObjectReference{Kind: "DockerImage", Name: "mysql:latest"},
					Generation:   &one,
					ImportPolicy: api.TagImportPolicy{Scheduled: true},
				},
			},
		},
		Status: api.ImageStreamStatus{
			Tags: map[string]api.TagEventList{
				"default": {Items: []api.TagEvent{{Generation: 1}}},
			},
		},
	}
	successfulImport := &api.ImageStreamImport{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "other"},
		Spec: api.ImageStreamImportSpec{
			Import: true,
			Images: []api.ImageImportSpec{{From: kapi.ObjectReference{Kind: "DockerImage", Name: "mysql:latest"}}},
		},
		Status: api.ImageStreamImportStatus{
			Images: []api.ImageImportStatus{{
				Status: metav1.Status{Status: metav1.StatusSuccess},
				Image:  &api.Image{},
			}},
		},
	}

	// queue, but don't import the stream
	if err := b.Handle(stream); err != nil {
		t.Fatal(err)
	}
	if b.scheduler.Len() != 1 {
		t.Fatalf("should have scheduled: %#v", b.scheduler)
	}
	if len(fake.Actions()) != 0 {
		t.Fatalf("should have made no calls: %#v", fake)
	}

	// run a background import
	fake = client.NewSimpleFake(stream, successfulImport)
	b.controller.streams = fake
	b.scheduler.RunOnce()
	if b.scheduler.Len() != 1 {
		t.Fatalf("should have left item in scheduler: %#v", b.scheduler)
	}
	if len(fake.Actions()) != 2 || !fake.Actions()[0].Matches("get", "imagestreams") || !fake.Actions()[1].Matches("create", "imagestreamimports") {
		t.Fatalf("invalid actions: %#v", fake.Actions())
	}
	var key, value interface{}
	for k, v := range b.scheduler.Map() {
		key, value = k, v
		break
	}

	// encountering a not found error for image streams should drop the controller
	fake = &client.Fake{}
	fake.AddReactor("*", "*", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, kerrors.NewNotFound(api.Resource("imagestreams"), "test")
	})
	b.controller.streams = fake
	b.scheduler.RunOnce()
	if b.scheduler.Len() != 0 {
		t.Fatalf("should have removed item in scheduler: %#v", b.scheduler)
	}
	if len(fake.Actions()) != 1 || !fake.Actions()[0].Matches("get", "imagestreams") {
		t.Fatalf("invalid actions: %#v", fake.Actions())
	}

	// requeue the stream with a new resource version
	stream.ResourceVersion = "2"
	if err := b.Handle(stream); err != nil {
		t.Fatal(err)
	}
	if b.scheduler.Len() != 1 {
		t.Fatalf("should have scheduled: %#v", b.scheduler)
	}

	// simulate a race where another caller attempts to dequeue the item
	if b.scheduler.Remove(key, value) {
		t.Fatalf("should not have removed %s: %#v", key, b.scheduler)
	}
	if b.scheduler.Len() != 1 {
		t.Fatalf("should have left scheduled: %#v", b.scheduler)
	}
}