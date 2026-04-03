@lezz
@z3-epistemic
@domain-permissions
Feature: Runtime Permissions and User Provisioning
  lezz declares what access it needs, prompts before acquiring it,
  and documents what was granted and why

  Scenario: lezz operates without root for user-scoped install
    Given lezz is run as a normal user
    When lezz installs and manages tools
    Then all files are written under the user's home directory
    And lezz does not invoke sudo or request elevated privileges

  Scenario: lezz prompts before creating a system user for daemon mode
    Given the user requests daemon configuration via "lezz service install"
    And no runtime user exists yet
    When lezz determines a system user is needed
    Then lezz explains why the user is needed before creating it
    And lezz prompts for confirmation
    And if the user declines, lezz falls back to running under the invoking user

  Scenario: lezz documents granted permissions
    Given lezz has been granted permission to create "lezz-svc" system user
    Then lezz writes a permissions manifest recording what was created
    And the manifest includes the reason each permission was granted
    And the manifest is readable by the user without root

  Scenario: lezz revokes all created permissions on uninstall
    Given lezz created a system user "lezz-svc" during setup
    When I run "lezz uninstall"
    Then lezz offers to remove the "lezz-svc" user
    And lezz offers to remove all daemon configurations
    And nothing is removed without explicit confirmation

  Scenario: lezz declares required permissions before requesting them
    Given lezz is starting for the first time
    When lezz determines it needs filesystem and network access
    Then lezz presents a permission summary before doing anything
    And each entry states what is needed and why
    And the user may accept all, select individually, or abort

  Scenario: lezz reports current permission state on request
    Given lezz has created a system user and two daemon units
    When I run "lezz permissions status"
    Then lezz lists all permissions it holds
    And lezz lists all system resources it created
    And the output includes the date each permission was granted
