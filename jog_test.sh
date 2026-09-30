#!/bin/bash

SLAVE=0          # Change if your slave ID is different
SPEED=20000        # Very safe low velocity for test
TIME=3         # Jog duration in seconds

echo "Setting velocity mode..."
sudo ethercat download -p$SLAVE --type int8  0x6060 0 3

echo "Enabling drive..."
sudo ethercat download -p$SLAVE --type uint16 0x6040 0 0x0006
sudo ethercat download -p$SLAVE --type uint16 0x6040 0 0x0007
sudo ethercat download -p$SLAVE --type uint16 0x6040 0 0x000F

echo "Jog forward for $TIME seconds..."
sudo ethercat download -p$SLAVE --type int16 0x6042 0 $SPEED

sleep $TIME

echo "Stopping motor..."
sudo ethercat download -p$SLAVE --type int16 0x6042 0 0

echo "Test jog complete."

