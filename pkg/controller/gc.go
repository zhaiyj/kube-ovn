package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"

	"github.com/ovn-org/libovsdb/ovsdb"

	"github.com/kubeovn/kube-ovn/pkg/ovsdb/ovnnb"

	"github.com/scylladb/go-set/strset"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/kubeovn/kube-ovn/pkg/ovs"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

var lastNoPodLSP map[string]bool

func (c *Controller) gc() error {
	gcFunctions := []func() error{
		c.gcNode,
		c.gcChassis,
		c.gcLogicalSwitch,
		c.gcCustomLogicalRouter,
		c.gcLogicalSwitchPort,
		c.gcLoadBalancer,
		c.gcPortGroup,
		c.gcStaticRoute,
		c.gcVpcNatGateway,
		c.gcLogicalRouterPort,
		c.gcDNS,
	}
	for _, gcFunc := range gcFunctions {
		if err := gcFunc(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) gcLogicalRouterPort() error {
	klog.Infof("start to gc logical router port")
	vpcs, err := c.vpcsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list vpc, %v", err)
		return err
	}

	exceptPeerPorts := strset.New()
	for _, vpc := range vpcs {
		for _, peer := range vpc.Status.VpcPeerings {
			exceptPeerPorts.Add(fmt.Sprintf("%s-%s", vpc.Name, peer))
		}
	}

	if err = c.ovnClient.DeleteLogicalRouterPorts(nil, logicalRouterPortFilter(exceptPeerPorts)); err != nil {
		klog.Errorf("delete non-existent peer logical router port: %v", err)
		return err
	}
	return nil
}

func (c *Controller) gcVpcNatGateway() error {
	klog.Infof("start to gc vpc nat gateway")
	gws, err := c.vpcNatGatewayLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list vpc nat gateway, %v", err)
		return err
	}

	var gwDpNames []string
	for _, gw := range gws {
		_, err = c.vpcsLister.Get(gw.Spec.Vpc)
		if err != nil {
			if !k8serrors.IsNotFound(err) {
				klog.Errorf("failed to get vpc, %v", err)
				return err
			}
			if err = c.config.KubeOvnClient.KubeovnV1().VpcNatGateways().Delete(context.Background(), gw.Name, metav1.DeleteOptions{}); err != nil {
				klog.Errorf("failed to delete vpc nat gateway, %v", err)
				return err
			}
		}
		gwDpNames = append(gwDpNames, genNatGwDpName(gw.Name))
	}

	sel, _ := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: map[string]string{util.VpcNatGatewayLabel: "true"}})
	dps, err := c.config.KubeClient.AppsV1().Deployments(c.config.PodNamespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: sel.String(),
	})
	if err != nil {
		klog.Errorf("failed to list vpc nat gateway deployment, %v", err)
		return err
	}
	for _, dp := range dps.Items {
		if !util.ContainsString(gwDpNames, dp.Name) {
			err = c.config.KubeClient.AppsV1().Deployments(c.config.PodNamespace).Delete(context.Background(), dp.Name, metav1.DeleteOptions{})
			if err != nil {
				klog.Errorf("failed to delete vpc nat gateway deployment, %v", err)
				return err
			}
		}
	}
	return nil
}

func (c *Controller) gcLogicalSwitch() error {
	klog.Infof("start to gc logical switch")
	subnets, err := c.subnetsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list subnet, %v", err)
		return err
	}
	subnetNames := make([]string, 0, len(subnets))
	for _, s := range subnets {
		subnetNames = append(subnetNames, s.Name)
	}
	lss, err := c.ovnClient.ListLogicalSwitch(c.config.EnableExternalVpc, nil)
	if err != nil {
		klog.Errorf("failed to list logical switch, %v", err)
		return err
	}
	klog.Infof("ls in ovn %v", lss)
	klog.Infof("subnet in kubernetes %v", subnetNames)
	for _, ls := range lss {
		if ls.Name == util.InterconnectionSwitch || ls.Name == util.ExternalGatewaySwitch {
			continue
		}
		if !util.IsStringIn(ls.Name, subnetNames) {
			klog.Infof("gc subnet %s", ls)
			if err := c.handleDeleteLogicalSwitch(ls.Name); err != nil {
				klog.Errorf("failed to gc subnet %s, %v", ls.Name, err)
				return err
			}
		}
	}

	klog.Infof("start to gc dhcp options")
	dhcpOptions, err := c.ovnClient.ListDHCPOptions(c.config.EnableExternalVpc, nil)
	if err != nil {
		klog.Errorf("failed to list dhcp options, %v", err)
		return err
	}
	var uuidToDeleteList = []string{}
	for _, item := range dhcpOptions {
		ls := strings.TrimSuffix(item.ExternalIDs["ls"], util.DHCPLsNonRouterSuffix)
		if !util.IsStringIn(ls, subnetNames) {
			uuidToDeleteList = append(uuidToDeleteList, item.UUID)
		}
	}
	klog.Infof("gc dhcp options %v", uuidToDeleteList)
	if len(uuidToDeleteList) > 0 {
		if err = c.ovnClient.DeleteDHCPOptionsByUUIDs(uuidToDeleteList...); err != nil {
			klog.Errorf("failed to delete dhcp options by uuids, %v", err)
			return err
		}
	}
	return nil
}

func (c *Controller) gcCustomLogicalRouter() error {
	klog.Infof("start to gc logical router")
	vpcs, err := c.vpcsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list vpc, %v", err)
		return err
	}
	vpcNames := make([]string, 0, len(vpcs))
	for _, s := range vpcs {
		vpcNames = append(vpcNames, s.Name)
	}
	lrs, err := c.ovnClient.ListLogicalRouter(c.config.EnableExternalVpc, nil)
	if err != nil {
		klog.Errorf("failed to list logical router, %v", err)
		return err
	}
	klog.Infof("lr in ovn %v", lrs)
	klog.Infof("vpc in kubernetes %v", vpcNames)
	for _, lr := range lrs {
		if lr.Name == util.DefaultVpc {
			continue
		}
		if !util.IsStringIn(lr.Name, vpcNames) {
			klog.Infof("gc router %s", lr)
			if err := c.deleteVpcRouter(lr.Name); err != nil {
				klog.Errorf("failed to delete router %s, %v", lr, err)
				return err
			}
		}
	}
	return nil
}

func (c *Controller) gcNode() error {
	klog.Infof("start to gc nodes")
	nodes, err := c.nodesLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list node, %v", err)
		return err
	}
	nodeNames := make([]string, 0, len(nodes))
	for _, no := range nodes {
		nodeNames = append(nodeNames, no.Name)
	}
	ips, err := c.ipsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list ip, %v", err)
		return err
	}
	ipNodeNames := make([]string, 0, len(ips))
	for _, ip := range ips {
		if !strings.Contains(ip.Name, ".") {
			ipNodeNames = append(ipNodeNames, strings.TrimPrefix(ip.Name, "node-"))
		}
	}
	for _, no := range ipNodeNames {
		if !util.IsStringIn(no, nodeNames) {
			klog.Infof("gc node %s", no)
			if err := c.handleDeleteNode(no); err != nil {
				klog.Errorf("failed to gc node %s, %v", no, err)
				return err
			}
		}
	}
	return nil
}

func (c *Controller) gcLogicalSwitchPort() error {
	klog.Info("start to gc logical switch port")
	if err := c.markAndCleanLSP(); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	return c.markAndCleanLSP()
}

func (c *Controller) markAndCleanLSP() error {
	klog.V(4).Infof("start to gc logical switch ports")
	pods, err := c.podsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list ip, %v", err)
		return err
	}
	nodes, err := c.nodesLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list node, %v", err)
		return err
	}
	ipMap := strset.NewWithSize(len(pods) + len(nodes))
	for _, pod := range pods {
		if isStsPod, sts := isStatefulSetPod(pod); isStsPod {
			if isStatefulSetPodToDel(c.config.KubeClient, pod, sts) {
				continue
			}
		} else if !isPodAlive(pod) {
			continue
		}

		for k, v := range pod.Annotations {
			if !strings.Contains(k, util.AllocatedAnnotationSuffix) || v != "true" {
				continue
			}
			providerName := strings.ReplaceAll(k, util.AllocatedAnnotationSuffix, "")
			isProviderOvn, err := c.isOVNProvided(providerName, pod)
			if err != nil {
				klog.Errorf("determine if provider is ovn failed %v", err)
			}
			if !isProviderOvn {
				continue
			}
			ipMap.Add(ovs.PodNameToPortName(pod.Name, pod.Namespace, providerName))
		}
	}
	for _, node := range nodes {
		if node.Annotations[util.AllocatedAnnotation] == "true" {
			ipMap.Add(fmt.Sprintf("node-%s", node.Name))
		}
	}

	lsps, err := c.ovnClient.ListNormalLogicalSwitchPorts(c.config.EnableExternalVpc, nil)
	if err != nil {
		klog.Errorf("failed to list logical switch port, %v", err)
		return err
	}

	noPodLSP := map[string]bool{}
	lspMap := strset.NewWithSize(len(lsps))
	for _, lsp := range lsps {
		lspMap.Add(lsp.Name)
		if ipMap.Has(lsp.Name) {
			continue
		}

		if !lastNoPodLSP[lsp.Name] {
			noPodLSP[lsp.Name] = true
			continue
		}

		klog.Infof("gc logical switch port %s", lsp)
		if err := c.ovnClient.DeleteLogicalSwitchPort(lsp.Name); err != nil {
			klog.Errorf("failed to delete lsp %s, %v", lsp, err)
			return err
		}
		if err := c.config.KubeOvnClient.KubeovnV1().IPs().Delete(context.Background(), lsp.Name, metav1.DeleteOptions{}); err != nil {
			if !k8serrors.IsNotFound(err) {
				klog.Errorf("failed to delete ip %s, %v", lsp, err)
				return err
			}
		}

		if key := lsp.ExternalIDs["pod"]; key != "" {
			c.ipam.ReleaseAddressByPod(key)
		}
	}
	lastNoPodLSP = noPodLSP

	ipMap.Each(func(ipName string) bool {
		if !lspMap.Has(ipName) {
			klog.Errorf("lsp lost for pod %s, please delete the pod and retry", ipName)
		}
		return true
	})
	return nil
}

func (c *Controller) gcLoadBalancer() error {
	klog.Infof("start to gc loadbalancers")
	if !c.config.EnableLb {
		// remove lb from logical switch
		vpcs, err := c.vpcsLister.List(labels.Everything())
		if err != nil {
			return err
		}
		for _, cachedVpc := range vpcs {
			vpc := cachedVpc.DeepCopy()
			for _, subnetName := range vpc.Status.Subnets {
				subnet, err := c.subnetsLister.Get(subnetName)
				if err != nil {
					if k8serrors.IsNotFound(err) {
						continue
					}
					return err
				}
				if !isOvnSubnet(subnet) {
					continue
				}

				lbs := []string{vpc.Status.TcpLoadBalancer, vpc.Status.TcpSessionLoadBalancer, vpc.Status.UdpLoadBalancer, vpc.Status.UdpSessionLoadBalancer}
				if err := c.ovnClient.LogicalSwitchUpdateLoadBalancers(subnetName, ovsdb.MutateOperationDelete, lbs...); err != nil {
					return err
				}
			}

			vpc.Status.TcpLoadBalancer = ""
			vpc.Status.TcpSessionLoadBalancer = ""
			vpc.Status.UdpLoadBalancer = ""
			vpc.Status.UdpSessionLoadBalancer = ""
			bytes, err := vpc.Status.Bytes()
			if err != nil {
				return err
			}
			_, err = c.config.KubeOvnClient.KubeovnV1().Vpcs().Patch(context.Background(), vpc.Name, types.MergePatchType, bytes, metav1.PatchOptions{}, "status")
			if err != nil {
				return err
			}
		}

		// lbs will remove from logical switch automatically when delete lbs
		if err = c.ovnClient.DeleteLoadBalancers(nil); err != nil {
			klog.Errorf("delete all load balancers: %v", err)
			return err
		}
		return nil
	}

	svcs, err := c.servicesLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list svc, %v", err)
		return err
	}
	tcpVips := strset.NewWithSize(len(svcs) * 2)
	udpVips := strset.NewWithSize(len(svcs) * 2)
	sctpVips := strset.NewWithSize(len(svcs) * 2)
	tcpSessionVips := strset.NewWithSize(len(svcs) * 2)
	udpSessionVips := strset.NewWithSize(len(svcs) * 2)
	sctpSessionVips := strset.NewWithSize(len(svcs) * 2)
	for _, svc := range svcs {
		ips := util.ServiceClusterIPs(*svc)
		if v, ok := svc.Annotations[util.SwitchLBRuleVipsAnnotation]; ok {
			ips = strings.Split(v, ",")
		}

		for _, ip := range ips {
			for _, port := range svc.Spec.Ports {
				vip := util.JoinHostPort(ip, port.Port)
				switch port.Protocol {
				case corev1.ProtocolTCP:
					if svc.Spec.SessionAffinity == corev1.ServiceAffinityClientIP {
						tcpSessionVips.Add(vip)
					} else {
						tcpVips.Add(vip)
					}
				case corev1.ProtocolUDP:
					if svc.Spec.SessionAffinity == corev1.ServiceAffinityClientIP {
						udpSessionVips.Add(vip)
					} else {
						udpVips.Add(vip)
					}
				case corev1.ProtocolSCTP:
					if svc.Spec.SessionAffinity == corev1.ServiceAffinityClientIP {
						sctpSessionVips.Add(vip)
					} else {
						sctpVips.Add(vip)
					}
				}
			}
		}
	}

	vpcs, err := c.vpcsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list vpc, %v", err)
		return err
	}
	var vpcLbs []string
	for _, vpc := range vpcs {
		tcpLb, udpLb := vpc.Status.TcpLoadBalancer, vpc.Status.UdpLoadBalancer
		tcpSessLb, udpSessLb := vpc.Status.TcpSessionLoadBalancer, vpc.Status.UdpSessionLoadBalancer
		vpcLbs = append(vpcLbs, tcpLb, udpLb, tcpSessLb, udpSessLb)

		removeVIP := func(lbName string, svcVips *strset.Set) error {
			if lbName == "" {
				return nil
			}

			lb, err := c.ovnClient.GetLoadBalancer(lbName, true)
			if err != nil {
				klog.Errorf("get LB %s: %v", lbName, err)
				return err
			}
			if lb == nil {
				klog.Infof("load balancer %q not found", lbName)
				return nil
			}

			for vip := range lb.Vips {
				if !svcVips.Has(vip) {
					if err = c.ovnClient.LoadBalancerDeleteVip(lbName, vip); err != nil {
						klog.Errorf("failed to delete vip %s from LB %s: %v", vip, lbName, err)
						return err
					}
				}
			}
			return nil
		}

		if err = removeVIP(tcpLb, tcpVips); err != nil {
			return err
		}
		if err = removeVIP(tcpSessLb, tcpSessionVips); err != nil {
			return err
		}
		if err = removeVIP(udpLb, udpVips); err != nil {
			return err
		}
		if err = removeVIP(udpSessLb, udpSessionVips); err != nil {
			return err
		}
	}

	// delete lbs
	if err = c.ovnClient.DeleteLoadBalancers(func(lb *ovnnb.LoadBalancer) bool {
		return !util.ContainsString(vpcLbs, lb.Name)
	}); err != nil {
		klog.Errorf("delete load balancers: %v", err)
		return err
	}

	return nil
}

func (c *Controller) gcPortGroup() error {
	klog.Infof("start to gc network policy")

	npNames := strset.New()

	if c.config.EnableNP {
		nps, err := c.npsLister.List(labels.Everything())
		if err != nil {
			klog.Errorf("failed to list network policy, %v", err)
			return err
		}

		for _, np := range nps {
			npNames.Add(fmt.Sprintf("%s/%s", np.Namespace, np.Name))
		}

		// append node port group to npNames to avoid gc node port group
		nodes, err := c.nodesLister.List(labels.Everything())
		if err != nil {
			klog.Errorf("failed to list nodes, %v", err)
			return err
		}

		for _, node := range nodes {
			npNames.Add(fmt.Sprintf("%s/%s", "node", node.Name))
		}

		// append overlay subnets port group to npNames to avoid gc distributed subnets port group
		subnets, err := c.subnetsLister.List(labels.Everything())
		if err != nil {
			klog.Errorf("failed to list subnets %v", err)
			return err
		}
		for _, subnet := range subnets {
			if subnet.Spec.Vpc != c.config.ClusterRouter || (subnet.Spec.Vlan != "" && !subnet.Spec.LogicalGateway) || subnet.Name == c.config.NodeSwitch || subnet.Spec.GatewayType != kubeovnv1.GWDistributedType {
				continue
			}

			for _, node := range nodes {
				npNames.Add(fmt.Sprintf("%s/%s", subnet.Name, node.Name))
			}
		}

		// list all np port groups which externalIDs[np]!=""
		pgs, err := c.ovnClient.ListPortGroups(map[string]string{networkPolicyKey: ""})
		if err != nil {
			klog.Errorf("list np port group: %v", err)
			return err
		}

		for _, pg := range pgs {
			np := strings.Split(pg.ExternalIDs[networkPolicyKey], "/")
			if len(np) != 2 {
				// not np port group
				continue
			}
			if !npNames.Has(pg.ExternalIDs[networkPolicyKey]) {
				klog.Infof("gc port group '%s' network policy '%s'", pg.Name, pg.ExternalIDs[networkPolicyKey])
				c.deleteNpQueue.Add(pg.ExternalIDs[networkPolicyKey])
			}
		}
	}

	return nil
}

func (c *Controller) gcStaticRoute() error {
	klog.Infof("start to gc static routes")
	routes, err := c.ovnLegacyClient.GetStaticRouteList(util.DefaultVpc)
	if err != nil {
		klog.Errorf("failed to list static route %v", err)
		return err
	}
	for _, route := range routes {
		if route.Policy == ovs.PolicyDstIP || route.Policy == "" {
			if !c.ipam.ContainAddress(route.NextHop) {
				klog.Infof("gc static route %s %s %s", route.Policy, route.CIDR, route.NextHop)
				if err := c.ovnClient.DeleteLogicalRouterStaticRoute(c.config.ClusterRouter, &route.Policy, route.CIDR, route.NextHop); err != nil {
					klog.Errorf("failed to delete stale nexthop route %s, %v", route.NextHop, err)
				}
			}
		} else {
			if strings.Contains(route.CIDR, "/") {
				continue
			}
			if !c.ipam.ContainAddress(route.CIDR) {
				klog.Infof("gc static route %s %s %s", route.Policy, route.CIDR, route.NextHop)
				if err := c.ovnClient.DeleteLogicalRouterStaticRoute(c.config.ClusterRouter, &route.Policy, route.CIDR, route.NextHop); err != nil {
					klog.Errorf("failed to delete stale route %s, %v", route.NextHop, err)
				}
			}
		}
	}
	return nil
}

func (c *Controller) gcChassis() error {
	klog.Infof("start to gc chassis")
	chassises, err := c.ovnLegacyClient.GetAllChassis()
	if err != nil {
		klog.Errorf("failed to get all chassis, %v", err)
	}
	nodes, err := c.nodesLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list nodes, %v", err)
		return err
	}
	for _, chassis := range chassises {
		matched := true
		for _, node := range nodes {
			if chassis == node.Annotations[util.ChassisAnnotation] {
				matched = false
				break
			}
		}
		if matched {
			if err := c.ovnLegacyClient.DeleteChassisByName(chassis); err != nil {
				klog.Errorf("failed to delete chassis %s %v", chassis, err)
				return err
			}
		}
	}
	return nil
}

// Just for ECX
func (c *Controller) gcIP() {
	ips, err := c.ipsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list IP CR")
	}
	for _, ip := range ips {
		if ip.Spec.Namespace == "" {
			continue
		}
		_, err = c.podsLister.Pods(ip.Spec.Namespace).Get(ip.Spec.PodName)
		if err != nil && k8serrors.IsNotFound(err) {
			if err := c.config.KubeOvnClient.KubeovnV1().IPs().Delete(context.Background(), ip.Name, metav1.DeleteOptions{}); err != nil {
				klog.Errorf("failed to delete IP CR %s: %v", ip.Name, err)
			} else {
				c.ipam.ReleaseAddressByPod(fmt.Sprintf("%s/%s", ip.Spec.Namespace, ip.Spec.PodName))
			}
		}
	}
}

func (c *Controller) isOVNProvided(providerName string, pod *corev1.Pod) (bool, error) {
	ls := pod.Annotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, providerName)]
	subnet, err := c.config.KubeOvnClient.KubeovnV1().Subnets().Get(context.Background(), ls, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("parse annotation logical switch %s error %v", ls, err)
		return false, err
	}
	if !strings.HasSuffix(subnet.Spec.Provider, util.OvnProvider) {
		return false, nil
	}
	return true, nil
}

func (c *Controller) gcDNS() error {
	klog.Infof("start to gc dns")
	vpcs, err := c.vpcsLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list vpc %v", err)
		return err
	}
	vpcDnsRecordsMap := make(map[string]string)
	for _, vpc := range vpcs {
		if vpc.Annotations[util.DnsEnableAnnotation] == "true" {
			if dnsUuidStr := vpc.Annotations[util.DnsUuidAnnotation]; dnsUuidStr != "" {
				vpcDnsRecordsMap[dnsUuidStr] = ""
			}
		}
	}
	dnsList, err := c.ovnLegacyClient.ListDns()
	if err != nil {
		klog.Errorf("failed to list dns %v", err)
		return err
	}
	for _, dnsUuid := range dnsList {
		// if no exist, destroy dns
		if _, ok := vpcDnsRecordsMap[dnsUuid]; !ok {
			if err := c.ovnLegacyClient.DestroyDns(dnsUuid); err != nil {
				klog.Errorf("failed to destroy dns %v", err)
				return err
			}
		}
	}
	return nil
}

func logicalRouterPortFilter(exceptPeerPorts *strset.Set) func(lrp *ovnnb.LogicalRouterPort) bool {
	return func(lrp *ovnnb.LogicalRouterPort) bool {
		if exceptPeerPorts.Has(lrp.Name) {
			return false // ignore except lrp
		}

		return lrp.Peer != nil && len(*lrp.Peer) != 0
	}
}
