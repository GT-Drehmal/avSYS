package kube

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var getOptions = metav1.GetOptions{}

// StreamLogs follows a container's log, starting with the last tail lines.
func StreamLogs(ctx context.Context, cs kubernetes.Interface, namespace, pod, container string, tail int64) (io.ReadCloser, error) {
	return cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Follow:    true,
		TailLines: &tail,
	}).Stream(ctx)
}
