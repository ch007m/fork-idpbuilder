package get

import (
	"context"
	"fmt"
	"github.com/cnoe-io/idpbuilder/pkg/util"
	"io"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"

	"github.com/cnoe-io/idpbuilder/api/v1alpha1"
	"github.com/cnoe-io/idpbuilder/pkg/build"
	"github.com/cnoe-io/idpbuilder/pkg/k8s"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	argoCDAdminUsername          = "admin"
	argoCDInitialAdminSecretName = "argocd-initial-admin-secret"
	giteaAdminSecretName         = "gitea-credential"
)

var SecretsCmd = &cobra.Command{
	Use:          "secrets",
	Short:        "retrieve secrets from the cluster",
	Long:         ``,
	RunE:         getSecretsE,
	SilenceUsage: true,
}

// well known secrets that are part of the core packages
var (
	corePkgSecrets = map[string][]string{
		"argocd": []string{argoCDInitialAdminSecretName},
		"gitea":  []string{giteaAdminSecretName},
	}
)

type TemplateData struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Data      map[string]string `json:"data"`
}

type Secret struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Token     string `json:"token"`
}

func getSecretsE(cmd *cobra.Command, args []string) error {
	ctx, ctxCancel := context.WithCancel(cmd.Context())
	defer ctxCancel()
	kubeConfigPath := filepath.Join(homedir.HomeDir(), ".kube", "config")

	opts := build.NewBuildOptions{
		KubeConfigPath: kubeConfigPath,
		Scheme:         k8s.GetScheme(),
		CancelFunc:     ctxCancel,
	}

	b := build.NewBuild(opts)

	kubeConfig, err := b.GetKubeConfig()
	if err != nil {
		return fmt.Errorf("getting kube config: %w", err)
	}

	kubeClient, err := b.GetKubeClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("getting kube client: %w", err)
	}

	if len(packages) == 0 {
		return printAllPackageSecrets(ctx, os.Stdout, kubeClient, outputFormat)
	}

	return printPackageSecrets(ctx, os.Stdout, kubeClient, outputFormat)
}

func printAllPackageSecrets(ctx context.Context, outWriter io.Writer, kubeClient client.Client, format string) error {
	selector := labels.NewSelector()
	secrets := []Secret{}

	for k, v := range corePkgSecrets {
		for i := range v {
			secret, sErr := getCorePackageSecret(ctx, kubeClient, k, v[i])
			if sErr != nil {
				if errors.IsNotFound(sErr) {
					continue
				}
				return fmt.Errorf("getting secret %s in %s: %w", v[i], k, sErr)
			}
			secrets = append(secrets, generateSecret(secret))
		}
	}

	cnoeLabelSecrets, err := getSecretsByCNOELabel(ctx, kubeClient, selector)
	if err != nil {
		return fmt.Errorf("listing secrets: %w", err)
	}

	for i := range cnoeLabelSecrets.Items {
		secrets = append(secrets, generateSecret(cnoeLabelSecrets.Items[i]))
	}

	if len(secrets) == 0 {
		fmt.Println("no secrets found")
		return nil
	}
	return printSecretsOutput(outWriter, secrets, format)
}

func printPackageSecrets(ctx context.Context, outWriter io.Writer, kubeClient client.Client, format string) error {
	selector := labels.NewSelector()
	secrets := []Secret{}

	for i := range packages {
		p := packages[i]
		secretNames, ok := corePkgSecrets[p]
		if ok {
			for j := range secretNames {
				secret, sErr := getCorePackageSecret(ctx, kubeClient, p, secretNames[j])
				if sErr != nil {
					if errors.IsNotFound(sErr) {
						continue
					}
					return fmt.Errorf("getting secret %s in %s: %w", secretNames[j], p, sErr)
				}
				secrets = append(secrets, generateSecret(secret))
			}
			continue
		}

		req, pErr := labels.NewRequirement(v1alpha1.PackageNameLabelKey, selection.Equals, []string{p})
		if pErr != nil {
			return fmt.Errorf("building requirement for %s: %w", p, pErr)
		}

		pkgSelector := selector.Add(*req)

		cnoeLabelSecrets, err := getSecretsByCNOELabel(ctx, kubeClient, pkgSelector)
		if err != nil {
			return fmt.Errorf("listing secrets: %w", err)
		}

		for i := range cnoeLabelSecrets.Items {
			secrets = append(secrets, generateSecret(cnoeLabelSecrets.Items[i]))
		}

		if len(secrets) == 0 {
			fmt.Println("no secrets found")
			return nil
		}
	}

	return printSecretsOutput(outWriter, secrets, format)
}

func generateSecretTable(secretTable []Secret) metav1.Table {
	table := &metav1.Table{}
	table.ColumnDefinitions = []metav1.TableColumnDefinition{
		{Name: "Name", Type: "string"},
		{Name: "Namespace", Type: "string"},
		{Name: "Username", Type: "string"},
		{Name: "Password", Type: "string"},
		{Name: "Token", Type: "string"},
	}
	for _, secret := range secretTable {
		row := metav1.TableRow{
			Cells: []interface{}{
				secret.Name,
				secret.Namespace,
				secret.Username,
				secret.Password,
				secret.Token,
			},
		}
		table.Rows = append(table.Rows, row)
	}
	return *table
}

func printSecretsOutput(outWriter io.Writer, secrets []Secret, format string) error {
	switch format {
	case "json":
		return util.PrintDataAsJson(secrets, outWriter)
	case "yaml":
		return util.PrintDataAsYaml(secrets, outWriter)
	case "":
		return util.PrintTable(generateSecretTable(secrets), outWriter)
	default:
		return fmt.Errorf("output format %s is not supported", format)
	}
}

func generateSecret(s v1.Secret) Secret {
	secret := Secret{
		Name:      s.Name,
		Namespace: s.Namespace,
		Username:  string(s.Data["username"]),
		Password:  string(s.Data["password"]),
		Token:     string(s.Data["token"]),
	}
	return secret
}

func getSecretsByCNOELabel(ctx context.Context, kubeClient client.Client, l labels.Selector) (v1.SecretList, error) {
	req, err := labels.NewRequirement(v1alpha1.CLISecretLabelKey, selection.Equals, []string{v1alpha1.CLISecretLabelValue})
	if err != nil {
		return v1.SecretList{}, fmt.Errorf("building labels with key %s and value %s : %w", v1alpha1.CLISecretLabelKey, v1alpha1.CLISecretLabelValue, err)
	}

	secrets := v1.SecretList{}
	opts := client.ListOptions{
		LabelSelector: l.Add(*req),
		Namespace:     "", // find in all namespaces
	}
	return secrets, kubeClient.List(ctx, &secrets, &opts)
}

func getSecretByName(ctx context.Context, kubeClient client.Client, ns, name string) (v1.Secret, error) {
	s := v1.Secret{}
	return s, kubeClient.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, &s)
}

func getCorePackageSecret(ctx context.Context, kubeClient client.Client, ns, name string) (v1.Secret, error) {
	s, err := getSecretByName(ctx, kubeClient, ns, name)
	if err != nil {
		return v1.Secret{}, err
	}

	if name == argoCDInitialAdminSecretName && s.Data != nil {
		s.Data["username"] = []byte(argoCDAdminUsername)
	}
	return s, nil
}
