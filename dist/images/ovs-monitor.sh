#!/bin/bash
while true;do 
    vswitchd=$(ps -ef |grep openvswitch|grep ovs-vswitchd.pid |awk {'print $2'})
    if [ $? -ne 0 ]; then 
        echo "grep vswitchd error \n " >>  /tmp/output.txt
        sleep 2
	continue
    fi
    ovsdb=$(ps -ef |grep ovsdb-server |grep openvswitch |awk {'print $2'})
    if [ $? -ne 0 ]; then
        echo "grep ovsdb-server error \n " >>  /tmp/output.txt
        sleep 2
        continue
    fi
    
    if [ -n "$vswitchd" ] && [ -n "$ovsdb" ];then
        sleep 2
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

