package cluster

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type kubeconfigFile struct {
	APIVersion     string                   `yaml:"apiVersion"`
	Kind           string                   `yaml:"kind"`
	Clusters       []kubeconfigNamedCluster `yaml:"clusters"`
	Contexts       []kubeconfigNamedContext  `yaml:"contexts"`
	Users          []kubeconfigNamedUser    `yaml:"users"`
	CurrentContext string                   `yaml:"current-context"`
}

type kubeconfigNamedCluster struct {
	Name    string                 `yaml:"name"`
	Cluster map[string]interface{} `yaml:"cluster"`
}

type kubeconfigNamedContext struct {
	Name    string                 `yaml:"name"`
	Context map[string]interface{} `yaml:"context"`
}

type kubeconfigNamedUser struct {
	Name string                 `yaml:"name"`
	User map[string]interface{} `yaml:"user"`
}

func ExtractKubeconfig(client *SSHClient) (string, error) {
	output, err := client.Run("cat /etc/kubernetes/admin.conf")
	if err != nil {
		return "", fmt.Errorf("failed to read kubeconfig: %w", err)
	}
	return output, nil
}

func MergeKubeconfig(clusterName, kubeconfigYAML string) error {
	contextName := "vmm-" + clusterName

	// Parse the extracted kubeconfig
	var extracted kubeconfigFile
	if err := yaml.Unmarshal([]byte(kubeconfigYAML), &extracted); err != nil {
		return fmt.Errorf("failed to parse extracted kubeconfig: %w", err)
	}

	// Rename cluster, user, context
	if len(extracted.Clusters) > 0 {
		extracted.Clusters[0].Name = contextName
	}
	if len(extracted.Users) > 0 {
		extracted.Users[0].Name = contextName
	}
	if len(extracted.Contexts) > 0 {
		extracted.Contexts[0].Name = contextName
		extracted.Contexts[0].Context["cluster"] = contextName
		extracted.Contexts[0].Context["user"] = contextName
	}

	// Load existing kubeconfig
	kubeconfigPath := defaultKubeconfigPath()
	existing := &kubeconfigFile{
		APIVersion: "v1",
		Kind:       "Config",
	}
	if data, err := os.ReadFile(kubeconfigPath); err == nil {
		yaml.Unmarshal(data, existing)
	}

	// Merge: replace existing entries with same name, or append
	existing.Clusters = mergeNamedClusters(existing.Clusters, extracted.Clusters)
	existing.Contexts = mergeNamedContexts(existing.Contexts, extracted.Contexts)
	existing.Users = mergeNamedUsers(existing.Users, extracted.Users)
	existing.CurrentContext = contextName

	// Write back
	if err := os.MkdirAll(filepath.Dir(kubeconfigPath), 0755); err != nil {
		return fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}
	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal kubeconfig: %w", err)
	}
	return os.WriteFile(kubeconfigPath, data, 0600)
}

func RemoveKubeconfigContext(clusterName string) error {
	contextName := "vmm-" + clusterName
	kubeconfigPath := defaultKubeconfigPath()

	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil
	}
	var kc kubeconfigFile
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return nil
	}

	kc.Clusters = filterClusters(kc.Clusters, contextName)
	kc.Contexts = filterContexts(kc.Contexts, contextName)
	kc.Users = filterUsers(kc.Users, contextName)
	if kc.CurrentContext == contextName {
		kc.CurrentContext = ""
		if len(kc.Contexts) > 0 {
			kc.CurrentContext = kc.Contexts[0].Name
		}
	}

	out, err := yaml.Marshal(&kc)
	if err != nil {
		return err
	}
	return os.WriteFile(kubeconfigPath, out, 0600)
}

func defaultKubeconfigPath() string {
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return kc
	}
	home, _ := os.UserHomeDir()
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		home = filepath.Join("/home", sudoUser)
	}
	return filepath.Join(home, ".kube", "config")
}

func mergeNamedClusters(existing, incoming []kubeconfigNamedCluster) []kubeconfigNamedCluster {
	result := make([]kubeconfigNamedCluster, 0, len(existing))
	for _, e := range existing {
		replaced := false
		for _, n := range incoming {
			if e.Name == n.Name {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, e)
		}
	}
	return append(result, incoming...)
}

func mergeNamedContexts(existing, incoming []kubeconfigNamedContext) []kubeconfigNamedContext {
	result := make([]kubeconfigNamedContext, 0, len(existing))
	for _, e := range existing {
		replaced := false
		for _, n := range incoming {
			if e.Name == n.Name {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, e)
		}
	}
	return append(result, incoming...)
}

func mergeNamedUsers(existing, incoming []kubeconfigNamedUser) []kubeconfigNamedUser {
	result := make([]kubeconfigNamedUser, 0, len(existing))
	for _, e := range existing {
		replaced := false
		for _, n := range incoming {
			if e.Name == n.Name {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, e)
		}
	}
	return append(result, incoming...)
}

func filterClusters(clusters []kubeconfigNamedCluster, name string) []kubeconfigNamedCluster {
	var result []kubeconfigNamedCluster
	for _, c := range clusters {
		if c.Name != name {
			result = append(result, c)
		}
	}
	return result
}

func filterContexts(contexts []kubeconfigNamedContext, name string) []kubeconfigNamedContext {
	var result []kubeconfigNamedContext
	for _, c := range contexts {
		if c.Name != name {
			result = append(result, c)
		}
	}
	return result
}

func filterUsers(users []kubeconfigNamedUser, name string) []kubeconfigNamedUser {
	var result []kubeconfigNamedUser
	for _, u := range users {
		if u.Name != name {
			result = append(result, u)
		}
	}
	return result
}
