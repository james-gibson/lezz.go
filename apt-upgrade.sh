#!/bin/sh
export DEBIAN_FRONTEND=noninteractive
apt update
if apt -y -o DPKG::Options::="--force-confnew" upgrade > output.txt; then
    if [ -r /var/run/reboot-required ]; then
        echo Rebooting to finish the upgrade
        exit 2 # EXIT_REBOOT
    fi
else
    echo Upgrade failed:
    echo
    cat output.txt
    exit 1 # EXIT_FAILURE
fi
echo Upgrade complete
exit 0 # EXIT_SUCCESS
