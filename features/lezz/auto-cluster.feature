@lezz
@z2-relational
@domain-auto-cluster
Feature: Auto Base Cluster
  lezz auto launches a base cluster that starts empty and awaiting:
  managed tools are installed on demand, an existing discovery registry is
  reused instead of starting a competing cluster, and the smoke-alarm
  instances hardcode no targets — they are populated at runtime via the
  server's auto-refresh and remote-update paths.

  Scenario: lezz auto installs missing managed tools before launching
    Given "adhd" is not installed
    And "ocd-smoke-alarm" is not installed
    When I run "lezz auto"
    Then lezz installs adhd from its GitHub release
    And lezz installs ocd-smoke-alarm from its GitHub release
    And lezz launches the base cluster

  Scenario: lezz auto reuses a live discovery registry instead of a second cluster
    Given a lezz discovery registry is already running on port 19100
    And it advertises an existing cluster "demo-123"
    When I run "lezz auto"
    Then lezz does not start a second cluster
    And lezz prints the registered cluster endpoints
    And lezz tells the user to connect with "adhd --demo"

  Scenario: base cluster smoke-alarms start with no hardcoded targets
    Given no lezz discovery registry is running
    When I run "lezz auto"
    Then two ocd-smoke-alarm instances start
    And each instance config has an empty targets list
    And each instance enables discovery for auto-registration
    And each instance enables remote_agent for remote updates
    And each instance serves /healthz

  Scenario: base cluster adhd discovers endpoints from the registry
    Given no lezz discovery registry is running
    When I run "lezz auto"
    Then the discovery registry starts before adhd
    And adhd starts with "--demo" instead of a hardcoded config
    And adhd builds its smoke_alarm endpoints from the registry
    And the base cluster registers itself as "base-<pid>"

  Scenario: a second lezz auto run joins the existing registry
    Given a lezz auto base cluster is already running
    When I run "lezz auto" again
    Then the discovery registry is reused
    And no new smoke-alarm or adhd processes are spawned