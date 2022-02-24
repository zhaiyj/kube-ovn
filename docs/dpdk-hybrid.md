# Kube-OVN with nodes which run ovs-dpdk or ovs-kernel

This document describes how to run Kube-OVN with nodes which run ovs-dpdk or ovs-kernel

## Prerequisite
Node which run ovs-dpdk must have a net card bound to the dpdk driver.
Hugepages on the host.
## Label nodes used run ovs-dpdk
```bash
kubectl label nodes <node> ovn.kubernetes.io/ovs_dp_type="userspace"
```
## Set up net card
We use driverctl to persist the device driver configuration.
Here is an example to bind dpdk driver to a net card.
```bash
driverctl set-override 0000:00:0b.0 uio_pci_generic
```
For other drivers, please refer to https://www.dpdk.org/

## configrue node
Edit the configuration file on the node that needs to run ovs-dpdk. The configuration file needs to be placed in the /opt/ovs-config directory.


## Set up Kube-OVN
Just run install.sh

## How to use
Here is an example to create a pod to use userspace datapath.

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: ovn-dpdk
  namespace: default
spec:
  config: >-
    {"cniVersion": "0.3.0", "type": "kube-ovn", "server_socket":
    "/run/openvswitch/kube-ovn-daemon.sock", "provider": "ovn-dpdk.default.ovn",
    "vhost_user_socket_volume_name": "vhostuser-sockets",
    "vhost_user_socket_name": "sock"}
---
apiVersion: v1
kind: Pod
metadata:
  name: dpdk-pod
  namespace: default
spec:
    containers:
    - name: app
      ....
      ....
      resources:
        limits:
            cpu: '2'
            memory: 4Gi
            hugepages-2Mi: 2Gi
        request:
            cpu: '2'
            memory: 4Gi
            hugepages-2Mi: 2Gi
      volumeMounts:
        - name: vhostuser-sockets
          mountPath: /var/run/kubevirt/sockets
    nodeSelector:
        ovn.kubernetes.io/ovs_dp_type: userspace
    volumes:
    - name: vhostuser-sockets
      emptyDir: {}
```

# todo: app
