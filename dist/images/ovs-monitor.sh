#!/bin/bash
vswitchd=$(ps -ef |grep ovs-vswitchd |grep openvswitch |awk {'print $2'})
ovsdb=$(ps -ef |grep ovsdb-server |grep openvswitch |awk {'print $2'})
#使用pgrep查找进程
while true;do 
    if [ -n "$vswitchd" ] && [ -n "$ovsdb" ];then
        sleep 1
    else
        echo "$vswitchd and $ovsdb is not running. Starting it..." >> /tmp/output.txt
        log_pid=$(ps -ef |grep ovs-vswitchd.log |grep tail |awk {'print $2'})
        if [ -z "$log_pid" ] ; then
            echo "log进程不存在" >> /tmp/output.txt
        else 
            echo "kill ovs log pid $log_pid" >>  /tmp/output.txt
            kill -9 $log_pid
        fi
        #start_pid=$(ps -ef |grep "start-ovs-arm" |grep bash |awk {'print $2'})        
        #if [ -z "$start_pid" ] ; then
        #    echo "start进程不存在" >> /tmp/output.txt
        #else
        #    echo "kill ovs start pid $start_pid" >>  /tmp/output.txt
        #    kill -9 $start_pid
        #fi
        sleep 1
    fi
done

