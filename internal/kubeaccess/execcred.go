package kubeaccess

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// ExecAPIVersion is the exec credential API kubectl is asked to speak.
const ExecAPIVersion = "client.authentication.k8s.io/v1"

type execCredential struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Status     struct {
		Token               string `json:"token"`
		ExpirationTimestamp string `json:"expirationTimestamp"`
	} `json:"status"`
}

// WriteCredential prints one ExecCredential as a single line of JSON.
func WriteCredential(w io.Writer, iss Issued) error {
	var c execCredential
	c.APIVersion, c.Kind = ExecAPIVersion, "ExecCredential"
	c.Status.Token = iss.Token
	c.Status.ExpirationTimestamp = iss.ExpiresAt.UTC().Format(time.RFC3339)
	return json.NewEncoder(w).Encode(c)
}

// CheckExecInfo reads kubectl's KUBERNETES_EXEC_INFO value and refuses an
// API version other than v1, which this command cannot answer. An empty
// value is fine: the command can run by hand.
func CheckExecInfo(env string) error {
	if env == "" {
		return nil
	}
	var info struct {
		APIVersion string `json:"apiVersion"`
	}
	if err := json.Unmarshal([]byte(env), &info); err != nil {
		return fmt.Errorf("kubectl sent exec info that is not JSON: %w", err)
	}
	if info.APIVersion != ExecAPIVersion {
		return fmt.Errorf("kubectl asked for %s; this command speaks %s. Run 'runos kube access' again to rewrite the kubeconfig entry", info.APIVersion, ExecAPIVersion)
	}
	return nil
}
