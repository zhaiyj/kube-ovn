#!/bin/bash
while true;do
    ovsdb="" 
    vswitchd=$(pgrep -x "ovs-vswitchd")
    vswitchdb=$(pgrep -x "ovsdb-server")
    for i in $vswitchdb; do
        ovsdb=$(cat /proc/$i/cmdline|tr -d '\0'|awk '/ovsdb-server.pid/')
        if [ -n "$ovsdb" ] ; then
            break
        fi
    done     
    
    if [ -n "$vswitchd" ] && [ -n "$ovsdb" ];then
        sleep 15
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

