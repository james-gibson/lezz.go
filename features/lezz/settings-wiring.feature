@lezz
@z3-epistemic
@domain-settings
Feature: Editor settings wiring
  lezz writes its ACP agent into an editor's settings without taking the file
  over. The editor's file is hand-edited JWCC, so the only acceptable edit is
  the one member lezz owns.

  Background:
    Given Zed's settings file contains an agent "oc"
    And the file contains a comment and keys unrelated to agent servers

  Scenario: lezz wires the workspace agent into Zed
    Given the workspace agent binary can be resolved
    When I run "lezz agent setup zed"
    Then "agent_servers.workspace" is present
    And its command is the resolved workspace agent
    And its args name a driver, a root, and a session
    And "agent_servers.oc" is unchanged
    And lezz prints the settings path and the backup path

  Scenario: wiring preserves the rest of the file
    Given the workspace agent binary can be resolved
    When I run "lezz agent setup zed"
    Then the comment is still in the file
    And the unrelated keys are unchanged and in their original order
    And the file is still valid JWCC

  Scenario: wiring twice is wiring once
    Given the workspace agent is wired with command "C" and args "A"
    When I run "lezz agent setup zed" with command "C" and args "A"
    Then the file is byte-for-byte unchanged
    And no backup is written
    And lezz reports that the agent is already wired

  Scenario: lezz backs up before it writes
    Given the workspace agent binary can be resolved
    When I run "lezz agent setup zed"
    Then a "settings.json.bak-<timestamp>" copy holds the previous bytes
    And the copy has the same permissions as the original

  Scenario: a missing workspace agent is built
    Given no workspace agent can be resolved
    And the lab-safety source is available
    When I run "lezz agent setup zed"
    Then the workspace agent is built into lezz's bin directory
    And "agent_servers.workspace" is present
    And its command is the built binary

  Scenario: lezz refuses an agent it cannot resolve when building is disabled
    Given no workspace agent can be resolved
    When I run "lezz agent setup zed --no-build"
    Then the command fails
    And the settings file is unchanged
    And the error names "lezz agent build" and how to pass --command

  Scenario: a malformed settings file is refused, not repaired
    Given the settings file is not valid JSON or JWCC
    When I run "lezz agent setup zed"
    Then the command fails
    And the settings file is byte-for-byte unchanged

  Scenario: a dry run writes nothing
    Given the workspace agent binary can be resolved
    When I run "lezz agent setup zed --dry-run"
    Then lezz prints the entry it would write
    And the settings file is unchanged
    And no backup is written

  Scenario: removal takes only lezz's entry
    Given the workspace agent is wired alongside "oc"
    When I run "lezz agent remove zed --yes"
    Then "agent_servers.workspace" is absent
    And "agent_servers.oc" is unchanged
    And the comment is still in the file

  Scenario: removal needs confirmation
    Given the workspace agent is wired
    When I run "lezz agent remove zed"
    Then the command fails
    And the settings file is unchanged
    And the output explains that --yes is required

  Scenario: removing the last agent drops the empty agent_servers member
    Given "agent_servers" holds only the workspace agent
    When I run "lezz agent remove zed --yes"
    Then "agent_servers" is absent from the file
    And the unrelated keys are unchanged

  Scenario Outline: the settings path follows the platform convention
    Given XDG_CONFIG_HOME is "<xdg>"
    Then the settings path is "<path>"

    Examples:
      | xdg      | path                                  |
      | /tmp/xdg | /tmp/xdg/zed/settings.json            |
      | (unset)  | ~/.config/zed/settings.json           |

  Scenario: lezz writes only under the user's home
    Given lezz is run as a normal user
    When I run "lezz agent setup zed"
    Then the settings file is under the user's home directory
    And the backup is under the user's home directory
    And lezz does not invoke sudo
