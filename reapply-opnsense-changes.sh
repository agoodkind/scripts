#!/usr/bin/env sh

# Check if running as root
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: This script must be run as root"
    exit 1
fi

echo "=== Reapplying custom OPNsense changes ==="
echo ""

# 1. Dhclient backgrounding on boot
echo "1. Dhclient backgrounding on boot"
echo "   File: /usr/local/etc/inc/interfaces.inc"
echo "   Function: interface_dhcp_configure()"
if grep -q "AGOODKINDCUSTOM Dhclient backgrounding on boot" /usr/local/etc/inc/interfaces.inc; then
    echo "   ✓ Already applied"
else
    echo "   Applying change..."
    # Backup the file
    cp /usr/local/etc/inc/interfaces.inc /usr/local/etc/inc/interfaces.inc.backup.$(date +%Y%m%d_%H%M%S)
    
    # Find and replace the dhclient line, adding -b flag and comment
    sed -i '' "/mwexecf('\/sbin\/dhclient -c %s -p %s %s'/i\\
    /* AGOODKINDCUSTOM Dhclient backgrounding on boot so pending WPA_supplicant doesnt hang */
" /usr/local/etc/inc/interfaces.inc
    
    sed -i '' "s|mwexecf('/sbin/dhclient -c %s -p %s %s'|mwexecf('/sbin/dhclient -b -c %s -p %s %s'|" /usr/local/etc/inc/interfaces.inc
    
    if grep -q "AGOODKINDCUSTOM Dhclient backgrounding on boot" /usr/local/etc/inc/interfaces.inc && \
       grep -q "dhclient -b -c" /usr/local/etc/inc/interfaces.inc; then
        echo "   ✓ Successfully applied"
    else
        echo "   ✗ Failed to apply - check backup and apply manually"
    fi
fi
echo ""

# 2. Custom PREF64 for vlan064
echo "2. Custom PREF64 for vlan064"
echo "   File: /usr/local/etc/inc/plugins.inc.d/radvd.inc"
echo "   Function: radvd_configure_do()"
if [ -f "/usr/local/etc/inc/plugins.inc.d/radvd.inc" ]; then
    if grep -q "AGOODKINDCUSTOM: Add hardcoded PREF64 for vlan064" /usr/local/etc/inc/plugins.inc.d/radvd.inc; then
        echo "   ✓ Already applied"
    else
        echo "   Applying change..."
        # Backup the file
        cp /usr/local/etc/inc/plugins.inc.d/radvd.inc /usr/local/etc/inc/plugins.inc.d/radvd.inc.backup.$(date +%Y%m%d_%H%M%S)
        
        # Use awk to insert the code
        awk '
        /Generated RADVD config for manual assignment/ {
            manual_section = 1
        }
        {
            print
        }
        manual_section == 1 && !inserted && (/\$radvdconf \.= "interface {\$realif}/ || /\$radvdconf \.= "interface {\$device}/) {
            print "        /* AGOODKINDCUSTOM: Add hardcoded PREF64 for vlan064 */"
            print "        if ($realif == '\''vlan064'\'') {"
            print "            $radvdconf .= \"\\tnat64prefix 3d06:bad:b01:6464:ff9b::/96 {\\n\";"
            print "            $radvdconf .= \"\\t\\tAdvValidLifetime 1800;\\n\";"
            print "            $radvdconf .= \"\\t};\\n\";"
            print "        }"
            inserted = 1
        }
        ' /usr/local/etc/inc/plugins.inc.d/radvd.inc > /tmp/radvd.inc.tmp
        
        if [ -s /tmp/radvd.inc.tmp ]; then
            mv /tmp/radvd.inc.tmp /usr/local/etc/inc/plugins.inc.d/radvd.inc
        else
            echo "   ✗ Awk produced empty file, not applying"
        fi
        
        if grep -q "AGOODKINDCUSTOM: Add hardcoded PREF64 for vlan064" /usr/local/etc/inc/plugins.inc.d/radvd.inc; then
            echo "   ✓ Successfully applied"
        else
            echo "   ✗ Failed to apply - check backup and apply manually"
        fi
    fi
else
    echo "   ✗ File not found: /usr/local/etc/inc/plugins.inc.d/radvd.inc"
fi
echo ""

# 3. Remove bad SLAAC
echo "3. Remove bad SLAAC"
echo "   Creating/verifying scripts in /conf/"

# Create rm-bad-slaac-webpass.sh
cat > /conf/rm-bad-slaac-webpass.sh << 'EOF'
#!/usr/bin/env sh

iface="igc0"
slaac_addr="$(ifconfig "$iface" | grep 'inet6 .* autoconf' | awk '{print $2}')"

if [ -z "$slaac_addr" ]; then
	echo "not removing empty slaac"
	exit 1
fi

echo "removing autoconf slaac from $iface: $slaac_addr"
ifconfig "$iface" inet6 "$slaac_addr" -alias
ifconfig igc0 inet6 -accept_rtadv
EOF
chmod +x /conf/rm-bad-slaac-webpass.sh
echo "   ✓ Created /conf/rm-bad-slaac-webpass.sh"

# Create dhcp6c_wan_wrapper.sh
# Note: This assumes the interface logical name is 'wan'. 
# If using a different interface (e.g., opt1, opt2), adjust the script path accordingly.
cat > /conf/dhcp6c_wan_wrapper.sh << 'EOF'
#!/usr/bin/env sh

# Call OPNsense's generated script first
# The script path format is: /var/etc/dhcp6c_<interface>_script.sh
# where <interface> is the logical interface name (wan, opt1, opt2, etc.)
/var/etc/dhcp6c_wan_script.sh

# Then remove SLAAC after DHCPv6 operations complete
if [ "$REASON" = "INFOREQ" ] || [ "$REASON" = "REBIND" ] || [ "$REASON" = "RENEW" ] || [ "$REASON" = "REQUEST" ]; then
    /conf/rm-bad-slaac-webpass.sh
fi
EOF
chmod +x /conf/dhcp6c_wan_wrapper.sh
echo "   ✓ Created /conf/dhcp6c_wan_wrapper.sh"

# Check if syshook exists
if [ -L "/usr/local/etc/rc.syshook.d/start/94-badslaac" ]; then
    echo "   ✓ Syshook already configured: /usr/local/etc/rc.syshook.d/start/94-badslaac"
else
    echo "   Creating syshook link..."
    ln -s /conf/rm-bad-slaac-webpass.sh /usr/local/etc/rc.syshook.d/start/94-badslaac
    echo "   ✓ Created syshook: /usr/local/etc/rc.syshook.d/start/94-badslaac"
fi

echo ""
echo "=== Post-upgrade verification checklist ==="
echo "[ ] Verify #1 & #2 applied successfully (backups created with timestamp)"
echo "[ ] GUI: Interfaces > [WEBPASS] > DHCPv6 > Advanced Configuration > adv_dhcp6_interface_statement_script = /conf/dhcp6c_wan_wrapper.sh"
echo "[ ] Test: Run 'configctl interface reconfigure <interface>' to regenerate config files"
echo "[ ] Test: Check /var/etc/dhcp6c_<interface>_script.sh references the correct wrapper"
echo "[ ] Test: Verify wrapper calls /conf/rm-bad-slaac-webpass.sh on appropriate REASON events"
echo "[ ] Test: Verify SLAAC is removed from igc0 after DHCP renewal"
echo "[ ] Test: Verify syshook /usr/local/etc/rc.syshook.d/start/94-badslaac exists and points to /conf/rm-bad-slaac-webpass.sh"
echo ""
echo "Note: Backups of modified files saved with .backup.<timestamp> extension"
