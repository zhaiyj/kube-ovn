#!/bin/bash
ovn_controller="ovn-controller"
#使用pgrep查找进程
while true;do
    if (pgrep -x "$ovn_controller" > /dev/null);then
        sleep 1
    else
        echo "$ovn_controller is not running. Starting it..."  >>  /tmp/output.txt
        log_pid=$(ps -ef |grep ovn-controller.log |grep tail |awk {'print $2'})
         if [ -z "$log_pid" ] ; then
            echo "log进程不存在" >> /tmp/output.txt
        else
            kill -9 $log_pid
            echo "kill log ovn pid $log_pid" >>  /tmp/output.txt
        fi
        #start_pid=$(ps -ef |grep "start-ovn-arm" |grep bash |awk {'print $2'})
        #if [ -z "$start_pid" ] ; then
        #    echo "start进程不存在" >> /tmp/output.txt
        #else
        #    echo "kill start ovn pid $start_pid" >>  /tmp/output.txt
        #    kill -9 $start_pid
        #fi 
        sleep 1
    fi
done

