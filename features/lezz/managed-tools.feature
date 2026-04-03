@lezz
@z0-physical
@domain-managed-tools
Feature: Managed Tool Lifecycle
  lezz installs and launches its managed suite — adhd, ocd-smoke-alarm, and tuner

  Scenario: Managed tool is invocable after install
    Given lezz is installed
    When lezz installs "adhd"
    Then the adhd binary is present in the lezz tool cache
    And "lezz run adhd" launches the adhd process

  Scenario: Managed tool is invocable after install — ocd-smoke-alarm
    Given lezz is installed
    When lezz installs "ocd-smoke-alarm"
    Then the ocd-smoke-alarm binary is present in the lezz tool cache
    And "lezz run ocd-smoke-alarm" launches the ocd-smoke-alarm process

  Scenario: Managed tool is invocable after install — tuner
    Given lezz is installed
    When lezz installs "tuner"
    Then the tuner binary is present in the lezz tool cache
    And "lezz run tuner" launches the tuner process

  Scenario: lezz installs a missing tool on first invocation
    Given lezz is installed
    And "adhd" has not been installed yet
    When I run "lezz run adhd"
    Then lezz downloads and installs adhd automatically
    And adhd is launched after install completes

  Scenario: Tool versions are managed independently
    Given lezz manages adhd at version 1.2.0
    And lezz manages ocd-smoke-alarm at version 2.0.1
    When ocd-smoke-alarm releases version 2.1.0
    Then lezz can update ocd-smoke-alarm without touching adhd
    And adhd remains at version 1.2.0

  Scenario: lezz passes arguments through to the managed tool
    Given lezz is installed
    And "adhd" is installed
    When I run "lezz run adhd --headless --config /tmp/adhd.yaml"
    Then adhd receives the flags "--headless --config /tmp/adhd.yaml"
    And lezz does not interpret or consume those flags

  Scenario: Running an unknown tool name fails clearly
    Given lezz is installed
    When I run "lezz run notarealtool"
    Then lezz exits with a non-zero status
    And the error message names the unknown tool
    And lezz lists the known managed tools
