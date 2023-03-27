package controller

import (
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

type vpcService struct {
	Name     string
	Vip      string
	Vpc      string
	Protocol v1.Protocol
}

func (c *Controller) enqueueAddService(obj interface{}) {
	if !c.isLeader() {
		return
	}
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	svc := obj.(*v1.Service)
	klog.V(3).Infof("enqueue add service %s", key)

	if c.config.EnableNP {
		var netpols []string
		if netpols, err = c.svcMatchNetworkPolicies(svc); err != nil {
			utilruntime.HandleError(err)
			return
		}

		for _, np := range netpols {
			c.updateNpQueue.Add(np)
		}
	}

	// if vpc enable dns
	vpcName := svc.Annotations[util.VpcAnnotation]
	if vpcName != "" {
		vpc, err := c.vpcsLister.Get(vpcName)
		if err != nil {
			klog.Errorf("failed to get vpc %s of lb, %v", vpcName, err)
			utilruntime.HandleError(err)
			return
		}
		if vpc.Annotations[util.DnsEnableAnnotation] == "true" {
			// set records to dns
			if err := c.setDnsRecords(vpc, svc.Name, svc.Spec.ClusterIP); err != nil {
				klog.Errorf("svc %s in namespace %v, failed to create dns_records and set to logical_switch %v", svc.Name, svc.Namespace, err)
				utilruntime.HandleError(err)
				return
			}
		}
	}

}

func (c *Controller) enqueueDeleteService(obj interface{}) {
	if !c.isLeader() {
		return
	}
	svc := obj.(*v1.Service)
	//klog.V(3).Infof("enqueue delete service %s/%s", svc.Namespace, svc.Name)
	klog.Infof("enqueue delete service %s/%s", svc.Namespace, svc.Name)

	vip, ok := svc.Annotations[util.SwitchLBRuleVipsAnnotation]
	if ok || svc.Spec.ClusterIP != v1.ClusterIPNone && svc.Spec.ClusterIP != "" {

		if c.config.EnableNP {
			var netpols []string
			var err error
			if netpols, err = c.svcMatchNetworkPolicies(svc); err != nil {
				utilruntime.HandleError(err)
				return
			}

			for _, np := range netpols {
				c.updateNpQueue.Add(np)
			}
		}
		for _, port := range svc.Spec.Ports {
			vpcSvc := &vpcService{
				Name:     svc.Name,
				Vip:      fmt.Sprintf("%s:%d", svc.Spec.ClusterIP, port.Port),
				Protocol: port.Protocol,
				Vpc:      svc.Annotations[util.VpcAnnotation],
			}
			if vip != "" {
				vpcSvc.Vip = fmt.Sprintf("%s:%d", vip, port.Port)
			}
			klog.Infof("delete vpc service %v", vpcSvc)
			c.deleteServiceQueue.Add(vpcSvc)
		}
	}
}

func (c *Controller) enqueueUpdateService(old, new interface{}) {
	if !c.isLeader() {
		return
	}
	oldSvc := old.(*v1.Service)
	newSvc := new.(*v1.Service)
	if oldSvc.ResourceVersion == newSvc.ResourceVersion {
		return
	}

	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(new); err != nil {
		utilruntime.HandleError(err)
		return
	}
	klog.V(3).Infof("enqueue update service %s", key)
	c.updateServiceQueue.Add(key)
}

func (c *Controller) runDeleteServiceWorker() {
	for c.processNextDeleteServiceWorkItem() {
	}
}

func (c *Controller) runUpdateServiceWorker() {
	for c.processNextUpdateServiceWorkItem() {
	}
}

func (c *Controller) processNextDeleteServiceWorkItem() bool {
	obj, shutdown := c.deleteServiceQueue.Get()

	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer c.deleteServiceQueue.Done(obj)
		var vpcSvc *vpcService
		var ok bool
		if vpcSvc, ok = obj.(*vpcService); !ok {
			c.deleteServiceQueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected vpcService in workqueue but got %#v", obj))
			return nil
		}

		if err := c.handleDeleteService(vpcSvc); err != nil {
			c.deleteServiceQueue.AddRateLimited(obj)
			return fmt.Errorf("error syncing '%s': %s, requeuing", vpcSvc.Vip, err.Error())
		}
		c.deleteServiceQueue.Forget(obj)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}
	return true
}

func (c *Controller) processNextUpdateServiceWorkItem() bool {
	obj, shutdown := c.updateServiceQueue.Get()

	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer c.updateServiceQueue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			c.updateServiceQueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		if err := c.handleUpdateService(key); err != nil {
			c.updateServiceQueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		c.updateServiceQueue.Forget(obj)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}
	return true
}

func (c *Controller) handleDeleteService(service *vpcService) error {
	svcs, err := c.servicesLister.Services(v1.NamespaceAll).List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list svc, %v", err)
		return err
	}
	for _, svc := range svcs {
		if svc.Spec.ClusterIP == parseVipAddr(service.Vip) {
			return nil
		}
	}

	vpc, err := c.vpcsLister.Get(service.Vpc)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get vpc '%s', %v", service.Vpc, err)
		return err
	}

	// if vpc enable dns
	if vpc.Annotations[util.VpcEnableOvnLbAnnotation] == "true" {

		vpcLbConfig := c.GenVpcLoadBalancer(service.Vpc)
		vip := service.Vip
		if service.Protocol == v1.ProtocolTCP {
			if err := c.ovnClient.DeleteLoadBalancerVip(vip, vpcLbConfig.TcpLoadBalancer); err != nil {
				klog.Errorf("failed to delete vip %s from tcp lb, %v", vip, err)
				return err
			}
			if err := c.ovnClient.DeleteLoadBalancerVip(vip, vpcLbConfig.TcpSessLoadBalancer); err != nil {
				klog.Errorf("failed to delete vip %s from tcp session lb, %v", vip, err)
				return err
			}
		} else {
			if err := c.ovnClient.DeleteLoadBalancerVip(vip, vpcLbConfig.UdpLoadBalancer); err != nil {
				klog.Errorf("failed to delete vip %s from udp lb, %v", vip, err)
				return err
			}
			if err := c.ovnClient.DeleteLoadBalancerVip(vip, vpcLbConfig.UdpSessLoadBalancer); err != nil {
				klog.Errorf("failed to delete vip %s from udp session lb, %v", vip, err)
				return err
			}
		}
	}

	// if vpc enable dns
	if vpc.Annotations[util.DnsEnableAnnotation] == "true" {
		// delete records from dns
		if err := c.removeDnsRecords(vpc, service.Name); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) handleUpdateService(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}
	klog.V(5).Infof("update svc %s/%s", namespace, name)
	svc, err := c.servicesLister.Services(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	ip := svc.Spec.ClusterIP
	if ip == "" || ip == v1.ClusterIPNone {
		return nil
	}

	tcpVips := []string{}
	udpVips := []string{}
	vpcName := svc.Annotations[util.VpcAnnotation]
	if vpcName == "" {
		vpcName = util.DefaultVpc
	}
	vpc, err := c.vpcsLister.Get(vpcName)
	if err != nil {
		klog.Errorf("failed to get vpc %s of lb, %v", vpcName, err)
		return err
	}
	if vpc.Annotations[util.VpcEnableOvnLbAnnotation] == "true" {
		tcpLb, udpLb := vpc.Status.TcpLoadBalancer, vpc.Status.UdpLoadBalancer
		oTcpLb, oUdpLb := vpc.Status.TcpSessionLoadBalancer, vpc.Status.UdpSessionLoadBalancer
		if svc.Spec.SessionAffinity == v1.ServiceAffinityClientIP {
			tcpLb, udpLb = vpc.Status.TcpSessionLoadBalancer, vpc.Status.UdpSessionLoadBalancer
			oTcpLb, oUdpLb = vpc.Status.TcpLoadBalancer, vpc.Status.UdpLoadBalancer
		}

		for _, port := range svc.Spec.Ports {
			if port.Protocol == v1.ProtocolTCP {
				if util.CheckProtocol(ip) == kubeovnv1.ProtocolIPv6 {
					tcpVips = append(tcpVips, fmt.Sprintf("[%s]:%d", ip, port.Port))
				} else {
					tcpVips = append(tcpVips, fmt.Sprintf("%s:%d", ip, port.Port))
				}
			} else if port.Protocol == v1.ProtocolUDP {
				if util.CheckProtocol(ip) == kubeovnv1.ProtocolIPv6 {
					udpVips = append(udpVips, fmt.Sprintf("[%s]:%d", ip, port.Port))
				} else {
					udpVips = append(udpVips, fmt.Sprintf("%s:%d", ip, port.Port))
				}
			}
		}
		// for service update
		lbUuid, err := c.ovnClient.FindLoadbalancer(tcpLb)
		if err != nil {
			klog.Errorf("failed to get lb %v", err)
			return err
		}
		vips, err := c.ovnClient.GetLoadBalancerVips(lbUuid)
		if err != nil {
			klog.Errorf("failed to get tcp lb vips %v", err)
			return err
		}
		klog.V(3).Infof("exist tcp vips are %v", vips)
		for _, vip := range tcpVips {
			if err := c.ovnClient.DeleteLoadBalancerVip(vip, oTcpLb); err != nil {
				klog.Errorf("failed to delete lb %s form %s, %v", vip, oTcpLb, err)
				return err
			}
			if _, ok := vips[vip]; !ok {
				klog.Infof("add vip %s to tcp lb %s", vip, oTcpLb)
				c.updateEndpointQueue.Add(key)
				break
			}
		}

		for vip := range vips {
			if parseVipAddr(vip) == ip && !util.IsStringIn(vip, tcpVips) {
				klog.Infof("remove stall vip %s", vip)
				err := c.ovnClient.DeleteLoadBalancerVip(vip, tcpLb)
				if err != nil {
					klog.Errorf("failed to delete vip %s from tcp lb %v", vip, err)
					return err
				}
			}
		}

		lbUuid, err = c.ovnClient.FindLoadbalancer(udpLb)
		if err != nil {
			klog.Errorf("failed to get lb %v", err)
			return err
		}
		vips, err = c.ovnClient.GetLoadBalancerVips(lbUuid)
		if err != nil {
			klog.Errorf("failed to get udp lb vips %v", err)
			return err
		}
		klog.Infof("exist udp vips are %v", vips)
		for _, vip := range udpVips {
			if err := c.ovnClient.DeleteLoadBalancerVip(vip, oUdpLb); err != nil {
				klog.Errorf("failed to delete lb %s form %s, %v", vip, oUdpLb, err)
				return err
			}
			if _, ok := vips[vip]; !ok {
				klog.Infof("add vip %s to udp lb %s", vip, oUdpLb)
				c.updateEndpointQueue.Add(key)
				break
			}
		}

		for vip := range vips {
			if parseVipAddr(vip) == ip && !util.IsStringIn(vip, udpVips) {
				klog.Infof("remove stall vip %s", vip)
				if err := c.ovnClient.DeleteLoadBalancerVip(vip, udpLb); err != nil {
					klog.Errorf("failed to delete vip %s from udp lb %v", vip, err)
					return err
				}
			}
		}
	}

	// vpc dns
	if vpc.Annotations[util.DnsEnableAnnotation] == "true" {
		// set records to dns
		if err := c.setDnsRecords(vpc, svc.Name, svc.Spec.ClusterIP); err != nil {
			klog.Errorf("svc %s in namespace %v, failed to create dns_records in vpc '%s', %v", svc.Name, svc.Namespace, vpc.Name, err)
			return err
		}
	}
	return nil
}

func (c *Controller) setDnsRecords(vpc *kubeovnv1.Vpc, svcName, ip string) error {
	if dnsUuidsStr := vpc.Annotations[util.DnsUuidAnnotation]; dnsUuidsStr != "" {
		records := map[string]string{svcName: ip, fmt.Sprintf("%s.localdomain", svcName): ip}
		// set records to dns
		if err := c.ovnClient.SetDnsRecords(dnsUuidsStr, records); err != nil {
			klog.Errorf("failed to set records %v to dns %s, %v", svcName, dnsUuidsStr, err)
			return err
		}
	}
	return nil
}

func (c *Controller) removeDnsRecords(vpc *kubeovnv1.Vpc, svcName string) error {
	if dnsUuidsStr := vpc.Annotations[util.DnsUuidAnnotation]; dnsUuidsStr != "" {
		if err := c.ovnClient.RemoveDnsRecords(dnsUuidsStr, svcName, fmt.Sprintf("%s.localdomain", svcName)); err != nil {
			klog.Errorf("failed to remove records %v from dns %s, %v", svcName, dnsUuidsStr, err)
			return err
		}
	}

	return nil
}

// The type of vips is map, which format is like [fd00:10:96::11c9]:10665:[fc00:f853:ccd:e793::2]:10665,[fc00:f853:ccd:e793::3]:10665
// Parse key of map, [fd00:10:96::11c9]:10665 for example
func parseVipAddr(vipStr string) string {
	vip := strings.Split(vipStr, ":")[0]
	if strings.ContainsAny(vipStr, "[]") {
		vip = strings.Trim(strings.Split(vipStr, "]")[0], "[]")
	}
	return vip
}
