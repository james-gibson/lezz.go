@lezz
@z2-relational
@domain-daemon
Feature: Daemon Configuration
  lezz configures systemd units or cron jobs so that managed tools
  run automatically on boot or on a schedule

  Scenario: lezz installs a systemd service for a managed tool
    Given the host uses systemd
    And "adhd" is installed
    When I run "lezz service install adhd"
    Then a systemd unit file is written for adhd
    And the unit is enabled to start on boot
    And lezz confirms the service name and unit path

  Scenario: lezz installs a cron job when systemd is not available
    Given the host does not use systemd
    And "ocd-smoke-alarm" is installed
    When I run "lezz service install ocd-smoke-alarm"
    Then a cron entry is written for ocd-smoke-alarm
    And lezz confirms the cron schedule and entry

  Scenario: lezz prompts before overwriting an existing daemon config
    Given a systemd unit for "adhd" already exists
    When I run "lezz service install adhd"
    Then lezz shows the existing unit and the proposed replacement
    And lezz prompts for confirmation before overwriting
    And if the user declines, the existing unit is unchanged

  Scenario: lezz removes a daemon configuration
    Given a systemd unit for "adhd" exists and is enabled
    When I run "lezz service remove adhd"
    Then the unit is disabled
    And the unit file is removed
    And the running adhd service is stopped cleanly

  Scenario: Generated service unit runs as the correct user
    Given lezz has provisioned a runtime user "lezz-svc"
    When I run "lezz service install ocd-smoke-alarm"
    Then the systemd unit specifies "User=lezz-svc"
    And the unit does not run as root

  Scenario: Daemon config references the lezz-managed binary path
    Given "adhd" is installed in the lezz tool cache at "/home/user/.lezz/bin/adhd"
    When I run "lezz service install adhd"
    Then the generated unit ExecStart points to "/home/user/.lezz/bin/adhd"
    And the unit will survive lezz self-updates without manual editing
