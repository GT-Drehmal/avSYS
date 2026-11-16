package controller

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

const (
	// CurseForgeSecret holds the panel's CurseForge API key, as
	// CurseForgeSecretKey, in each namespace with a server that installs
	// from CurseForge. The game is given it from there.
	CurseForgeSecret    = "avsys-curseforge"
	CurseForgeSecretKey = "api-key"

	// CurseForgeDownloads is where a CurseForge modpack's files whose authors
	// allow downloads only on CurseForge itself are put by hand, for the
	// image to find.
	CurseForgeDownloads = "/data/downloads"
)

// UsesCurseForge says whether a server installs anything from CurseForge.
func UsesCurseForge(spec *avsysv1.MinecraftServerSpec) bool {
	if spec.Modpack != nil && spec.Modpack.Source == avsysv1.SourceCurseForge {
		return true
	}
	for _, m := range spec.Mods {
		if m.Source == avsysv1.SourceCurseForge {
			return true
		}
	}
	return false
}

// CurseForgeFileURL is where CurseForge keeps a file, by its ID and name:
// files/4593/548/name for file 4593548. "" without either.
func CurseForgeFileURL(fileID, name string) string {
	id, err := strconv.Atoi(fileID)
	if err != nil || id <= 0 || name == "" {
		return ""
	}
	return fmt.Sprintf("https://mediafilez.forgecdn.net/files/%d/%d/%s", id/1000, id%1000, url.PathEscape(name))
}

// curseForgeKeyEnv gives the game the panel's CurseForge API key from the
// Secret it is kept in, so that it is never written into the pod itself.
// Without one the image uses its own.
func curseForgeKeyEnv() corev1.EnvVar {
	optional := true
	return corev1.EnvVar{
		Name: "CF_API_KEY",
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: CurseForgeSecret},
			Key:                  CurseForgeSecretKey,
			Optional:             &optional,
		}},
	}
}

// EnsureCurseForgeSecret puts the CurseForge API key in a namespace, or
// takes it out where there is none: the Secret stays, empty, as the panel
// does not delete Secrets.
func EnsureCurseForgeSecret(ctx context.Context, c client.Client, namespace, key string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CurseForgeSecret, Namespace: namespace}}
	if key == "" {
		if err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if len(secret.Data) == 0 {
			return nil
		}
		secret.Data = map[string][]byte{}
		return c.Update(ctx, secret)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		secret.Data = map[string][]byte{CurseForgeSecretKey: []byte(key)}
		return nil
	})
	return err
}
