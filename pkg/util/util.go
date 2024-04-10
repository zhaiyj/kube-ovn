package util

import kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"

func IsProviderMatchNode(pn *kubeovnv1.ProviderNetwork, nodeName string) bool {
	if len(pn.Spec.IncludeNodes) > 0 {
		return ContainsString(pn.Spec.IncludeNodes, nodeName)
	}
	return !ContainsString(pn.Spec.ExcludeNodes, nodeName)
}
