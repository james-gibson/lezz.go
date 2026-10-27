@lezz
@z2-relational
@domain-sea-hub
Feature: One adhd session hub for the lezz.sea lab
  `lezz sea` runs one empty adhd for the session and gives the app its endpoint
  instead of letting the app guess `127.0.0.1:9090`. Every cluster's adhd joins
  that hub as a prime-plus, so a cluster appears as a light under the hub rather
  than as another port the app has to know. The hub outlives any one cluster and
  stops last, and it stays on loopback: it is a control surface, not an
  indicator.

  Scenario: lezz sea starts one hub and announces it on the channel
    Given lezz.sea is installed and verified
    And no adhd is listening on port 9090
    When I run "lezz sea"
    Then lezz starts one adhd as the session hub on 127.0.0.1:9090
    And the hub is started before lezz.sea
    And the hub config lives under the lab root
    And lezz announces the endpoint "http://127.0.0.1:9090/mcp" to the app

  Scenario: the hub falls back to a free port when 9090 is taken
    Given lezz.sea is installed and verified
    And another process is listening on port 9090
    When I run "lezz sea"
    Then lezz starts the session hub on a free port
    And lezz announces that port to the app
    And the announced endpoint answers "adhd.status"

  Scenario: the announced hub replaces the app's default endpoint
    Given lezz started lezz.sea with an isotope channel
    And the hub announced "http://127.0.0.1:9222/mcp"
    Then the app uses the announced endpoint as its default endpoint
    And the app does not probe 127.0.0.1:9090 for a cluster

  Scenario: a Finder launch keeps the operator's endpoint
    Given lezz.sea was launched without an isotope channel
    Then the app uses the endpoint the operator typed
    And the app does not wait for a hub announcement

  Scenario: a cluster's adhd is a prime-plus of the hub
    Given lezz sea is running with a session hub
    When the app asks for an isotope for cluster "demo-123"
    Then lezz starts that cluster's adhd with "--prime-plus"
    And it passes "--prime-addr" naming the hub's MCP endpoint
    And the reply carries both the hub endpoint and the cluster endpoint

  Scenario: a cluster joins the hub so its alarms appear as lights
    Given lezz sea is running with a session hub
    And cluster "demo-123" has a member alarm on port 18181
    When the app asks for an isotope for cluster "demo-123"
    Then lezz sends "adhd.cluster.join" to the hub for "demo-123"
    And the join names the member alarm as its first alarm
    And the hub's "adhd.lights.list" reports "demo-123/alarm-a"
    And the hub's "adhd.status" counts that light

  Scenario: a child's own lights are read through the hub, not a second port
    Given cluster "demo-123" has joined the session hub
    When the app reads the Scenarios row for "demo-123"
    Then it calls "adhd.proxy" on the hub
    And the proxy targets the cluster's own MCP endpoint
    And the proxied call is "adhd.lights.list"
    And the row shows the cluster's gherkin lights

  Scenario: a failed join does not fail the start
    Given lezz sea is running with a session hub
    And the hub refuses "adhd.cluster.join"
    When the app asks for an isotope for cluster "demo-123"
    Then the cluster's alarm and adhd still start
    And the reply names the cluster endpoint
    And lezz reports that the cluster did not join the hub

  Scenario: a stopped child disappears from the hub while the app stays connected
    Given cluster "demo-123" has joined the session hub
    And the app is showing the hub's lights
    When the app asks to stop "demo-123"
    Then the cluster's alarm and adhd are stopped
    And "demo-123/alarm-a" is absent from the hub's "adhd.lights.list"
    And the hub still answers "adhd.status"
    And the app remains connected to the hub

  Scenario: the hub stops last when the app exits
    Given lezz sea is running with a session hub
    And cluster "demo-123" is running
    When the lezz.sea process exits
    Then lezz stops every child alarm and child adhd first
    And lezz stops the session hub last
    And no pid file remains under the lab root

  Scenario: the hub stops last on Ctrl-C
    Given lezz sea is running with a session hub
    And cluster "demo-123" is running
    When the operator interrupts lezz with Ctrl-C
    Then lezz stops the cluster's alarm and adhd before the hub
    And lezz stops lezz.sea
    And lezz stops the session hub last

  Scenario: the channel light appears on the hub
    Given lezz sea is running with a session hub
    And the hub watches lezz's channel status endpoint
    Then the hub's "adhd.lights.list" reports a light for the channel
    And the light carries the app version and build
    When the app sends a request lezz refuses
    Then the channel light reports the refusal

  Scenario: the hub is not reachable beyond loopback
    Given the session hub is started
    Then it binds 127.0.0.1 only
    And it is not started with "--force"
    When adhd is asked to advertise the hub beyond loopback
    Then it refuses to advertise an unauthenticated MCP endpoint

  Scenario: a stop request never stops the hub
    Given lezz sea is running with a session hub
    And cluster "demo-123" has joined the hub
    When the app asks to stop "demo-123"
    Then the hub keeps answering "adhd.status"

  Scenario: discovered isotopes collapse by seed and then by isotope
    Given a smoke-alarm is running with three registered adhd isotopes
    And two of them declare the seed "lezz.go"
    And one of them declares no seed
    When the app lists discovered isotopes
    Then the two with the seed "lezz.go" share one seed group
    And each of them stays addressable as its own isotope
    And the one with no seed stays a named, ungrouped row
