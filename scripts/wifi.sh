#!/bin/bash

mv  /mnt/app/jamun/wpa_supplicant.conf /mnt/app/jamun/wpa_supplicant.conf.bak
echo "ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev
update_config=1
country="$1"

network={
    ssid=\"${2}\"
    psk=\"${3}\"
    key_mgmt=WPA-PSK
}" >> /mnt/app/jamun/wpa_supplicant.conf