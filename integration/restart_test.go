/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package integration

import (
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
	runtime "k8s.io/kubernetes/pkg/kubelet/apis/cri/runtime/v1alpha2"
)

// Restart test must run sequentially.

func TestContainerdRestart(t *testing.T) {
	type container struct {
		name  string
		id    string
		state runtime.ContainerState
	}
	type sandbox struct {
		name       string
		id         string
		state      runtime.PodSandboxState
		containers []container
	}
	ctx := context.Background()
	sandboxNS := "restart-containerd"
	sandboxes := []sandbox{
		{
			name:  "ready-sandbox",
			state: runtime.PodSandboxState_SANDBOX_READY,
			containers: []container{
				{
					name:  "created-container",
					state: runtime.ContainerState_CONTAINER_CREATED,
				},
				{
					name:  "running-container",
					state: runtime.ContainerState_CONTAINER_RUNNING,
				},
				{
					name:  "exited-container",
					state: runtime.ContainerState_CONTAINER_EXITED,
				},
			},
		},
		{
			name:  "notready-sandbox",
			state: runtime.PodSandboxState_SANDBOX_NOTREADY,
			containers: []container{
				{
					name:  "created-container",
					state: runtime.ContainerState_CONTAINER_CREATED,
				},
				{
					name:  "running-container",
					state: runtime.ContainerState_CONTAINER_RUNNING,
				},
				{
					name:  "exited-container",
					state: runtime.ContainerState_CONTAINER_EXITED,
				},
			},
		},
	}
	t.Logf("Make sure no sandbox is running before test")
	existingSandboxes, err := runtimeService.ListPodSandbox(&runtime.PodSandboxFilter{})
	require.NoError(t, err)
	require.Empty(t, existingSandboxes)

	t.Logf("Start test sandboxes and containers")
	for i := range sandboxes {
		s := &sandboxes[i]
		sbCfg := PodSandboxConfig(s.name, sandboxNS)
		sid, err := runtimeService.RunPodSandbox(sbCfg)
		require.NoError(t, err)
		defer func() {
			// Make sure the sandbox is cleaned up in any case.
			runtimeService.StopPodSandbox(sid)
			runtimeService.RemovePodSandbox(sid)
		}()
		s.id = sid
		for j := range s.containers {
			c := &s.containers[j]
			cfg := ContainerConfig(c.name, pauseImage,
				// Set pid namespace as per container, so that container won't die
				// when sandbox container is killed.
				WithPidNamespace(runtime.NamespaceMode_CONTAINER),
			)
			cid, err := runtimeService.CreateContainer(sid, cfg, sbCfg)
			require.NoError(t, err)
			// Reply on sandbox cleanup.
			c.id = cid
			switch c.state {
			case runtime.ContainerState_CONTAINER_CREATED:
			case runtime.ContainerState_CONTAINER_RUNNING:
				require.NoError(t, runtimeService.StartContainer(cid))
			case runtime.ContainerState_CONTAINER_EXITED:
				require.NoError(t, runtimeService.StartContainer(cid))
				require.NoError(t, runtimeService.StopContainer(cid, 10))
			}
		}
		if s.state == runtime.PodSandboxState_SANDBOX_NOTREADY {
			cntr, err := containerdClient.LoadContainer(ctx, sid)
			require.NoError(t, err)
			task, err := cntr.Task(ctx, nil)
			require.NoError(t, err)
			_, err = task.Delete(ctx, containerd.WithProcessKill)
			require.NoError(t, err)
		}
	}

	t.Logf("Kill containerd")
	require.NoError(t, KillProcess("containerd"))
	defer func() {
		assert.NoError(t, Eventually(func() (bool, error) {
			return ConnectDaemons() == nil, nil
		}, time.Second, 30*time.Second), "make sure containerd is running before test finish")
	}()

	t.Logf("Wait until containerd is killed")
	require.NoError(t, Eventually(func() (bool, error) {
		pid, err := PidOf("containerd")
		if err != nil {
			return false, err
		}
		return pid == 0, nil
	}, time.Second, 30*time.Second), "wait for containerd to be killed")

	t.Logf("Wait until containerd is restarted")
	require.NoError(t, Eventually(func() (bool, error) {
		return ConnectDaemons() == nil, nil
	}, time.Second, 30*time.Second), "wait for containerd to be restarted")

	t.Logf("Check sandbox and container state after restart")
	loadedSandboxes, err := runtimeService.ListPodSandbox(&runtime.PodSandboxFilter{})
	require.NoError(t, err)
	assert.Len(t, loadedSandboxes, len(sandboxes))
	loadedContainers, err := runtimeService.ListContainers(&runtime.ContainerFilter{})
	require.NoError(t, err)
	assert.Len(t, loadedContainers, len(sandboxes)*3)
	for _, s := range sandboxes {
		for _, loaded := range loadedSandboxes {
			if s.id == loaded.Id {
				assert.Equal(t, s.state, loaded.State)
				break
			}
		}
		for _, c := range s.containers {
			for _, loaded := range loadedContainers {
				if c.id == loaded.Id {
					assert.Equal(t, c.state, loaded.State)
					break
				}
			}
		}
	}

	t.Logf("Should be able to stop and remove sandbox after restart")
	for _, s := range sandboxes {
		assert.NoError(t, runtimeService.StopPodSandbox(s.id))
		assert.NoError(t, runtimeService.RemovePodSandbox(s.id))
	}
}
