#!/bin/bash
#version 0.961-N/HS-I

wifidev="wlan0" #device name to use. Default is wlan0.
ethdev="eth0" #Ethernet port to use with IP tables
# hotspot pwd can be found in /etc/hostapd/hostapd.conf

createHotspot()
{
    echo "Cleaning wifi files and Activating Hotspot"
    wpa_cli terminate >/dev/null 2>&1
    ip addr flush "$wifidev"
    ip link set dev "$wifidev" down
    rm -r /var/run/wpa_supplicant >/dev/null 2>&1
    echo "Creating Hotspot"
    ip link set dev "$wifidev" down
    ip a add 10.10.0.1/24 brd + dev "$wifidev"
    ip link set dev "$wifidev" up
    dhcpcd -k "$wifidev" >/dev/null 2>&1
    iptables -t nat -A POSTROUTING -o "$ethdev" -j MASQUERADE
    iptables -A FORWARD -i "$ethdev" -o "$wifidev" -m state --state RELATED,ESTABLISHED -j ACCEPT
    iptables -A FORWARD -i "$wifidev" -o "$ethdev" -j ACCEPT
    ifconfig wlan0 down
    systemctl start dnsmasq
    systemctl start hostapd
    echo 1 > /proc/sys/net/ipv4/ip_forward
}

killHotspot()
{
    echo "Shutting Down Hotspot"
    ip link set dev "$wifidev" down
    systemctl stop hostapd
    systemctl stop dnsmasq
    iptables -D FORWARD -i "$ethdev" -o "$wifidev" -m state --state RELATED,ESTABLISHED -j ACCEPT
    iptables -D FORWARD -i "$wifidev" -o "$ethdev" -j ACCEPT
    echo 0 > /proc/sys/net/ipv4/ip_forward
    ip addr flush dev "$wifidev"
    ip link set dev "$wifidev" up
    dhcpcd  -n "$wifidev" >/dev/null 2>&1
    ifconfig wlan0 up
    wpa_supplicant -B -i "$wifidev" -c /etc/wpa_supplicant/wpa_supplicant.conf >/dev/null 2>&1
}


while getopts a: flag
do
    case "${flag}" in
        a) action=${OPTARG};;
    esac
done

if [[ $action == "KILL" ]]
then
   killHotspot
else
   createHotspot
fi
