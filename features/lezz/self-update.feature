@lezz
@z1-temporal
@domain-self-update
Feature: lezz Self-Update
  lezz checks for and applies updates to itself in the background
  without interrupting running managed tools

  Scenario: lezz checks for a self-update on startup
    Given lezz starts
    When the update check completes
    Then lezz logs whether a newer version is available
    And startup is not blocked by the update check

  Scenario: lezz applies a self-update in the background
    Given a newer version of lezz is available
    When lezz performs a background self-update
    Then the lezz binary is replaced atomically
    And any currently running managed tools are not interrupted
    And the next invocation of lezz uses the new version

  Scenario: lezz verifies binary integrity before applying an update
    Given a newer lezz binary has been downloaded
    When lezz verifies the downloaded binary
    Then lezz checks the checksum against the published release manifest
    And lezz does not apply an update that fails verification

  Scenario: Self-update can be disabled
    Given lezz is configured with "auto_update: false"
    When lezz starts
    Then lezz does not check for or apply any updates
    And "lezz update" still works when invoked manually

  Scenario: lezz update — manual update check
    Given lezz is running
    When I run "lezz update"
    Then lezz checks for a newer release
    And if one is found, lezz downloads and applies it
    And if already up to date, lezz reports the current version

  Scenario: Update survives a network failure gracefully
    Given a newer version of lezz is available
    And the network is unavailable during the update download
    When lezz attempts a background self-update
    Then lezz logs the failure
    And the existing lezz binary is unchanged
    And lezz retries on the next startup
