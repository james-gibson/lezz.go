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

  Scenario: the hub is discovered on this host only until federation is enabled
    Given lezz sea is running with a session hub
    And no federation introducer is running
    Then the hub advertises "_adhd-isotope._tcp" on the loopback address
    And the advertisement names the hub's role
    And no smoke-alarm on the lab network lists the hub

  Scenario: enabling federation gives the hub an upstream registry instead of a wider bind
    Given a federation introducer is elected on port 5100
    When the hub enrols with that mesh
    Then its isotope is listed in "GET /membership"
    And that isotope is registered at the mesh member rung
    And the hub's MCP endpoint still binds 127.0.0.1 only

  Scenario: a stop request never stops the hub
    Given lezz sea is running with a session hub
    And cluster "demo-123" has joined the hub
    When the app asks to stop "demo-123"
    Then the hub keeps answering "adhd.status"

  Scenario: a rotation does not add a row
    Given the app shows the work group for route "lezz.go/peer"
    And its current claimant is "adhd-7c1d0a4e"
    When that claimant rotates to "adhd-9f2b31c8"
    Then the work group is still one row
    And the new claimant is shown as current
    And the previous claimant is shown as rotated out

  Scenario: a silent member ages out of its work group
    Given a work group for route "lezz.go/peer" has two claimants
    And one of them stops announcing
    When a rotation period passes without a claim from that member
    Then that member ages out of the work group
    And the work group still shows its remaining claimant
    And the work group row is not removed

  Scenario: a member that cannot name its route stays declared
    Given an advertisement carries an isotope identity and no route
    Then it is listed as a named, ungrouped row
    And it is not merged with a member that shares its endpoint

  Scenario: a rotation is a new name, not a new process
    Given an adhd instance announces itself as "adhd-7c1d0a4e"
    When the public tick advances to the next rotation
    Then it announces a new instance name carrying its new isotope
    And the previous name is withdrawn
    And the app still shows one work group for its route

  Scenario: a member that crashed ages out without a goodbye
    Given a work group has a claimant last announced two ticks ago
    Then the app ages that member out of the work group
    And it does not wait for a removal notification

  Scenario: an advertisement carries the claim, not the alphabet
    Given an adhd instance advertises over mDNS
    Then its instance name carries the task id and its member id
    And the name contains no colon
    And its TXT record carries "role", "trust_rung" and "scope"
    And neither the instance name nor the TXT record carries the seed array

  Scenario: a new seed starts a task, not a workspace
    Given the app shows workspace "lezz.go"
    When the workspace mints a new seed for its next action
    Then the workspace is still one row
    And a new task appears under its work group
    And no second workspace is created

  Scenario: participants attach to the task, not the workspace
    Given a task in workspace "lezz.go" has a claim from a person
    When an agent joins that same task
    Then the task shows both participants
    And it is still one task row
    And a second person on another task in the workspace is not shown under it

  Scenario: a workspace nobody shares is not on the wire
    Given a workspace is private and has not been shared
    Then no route is advertised for it
    And the session hub does not list it
    And nothing on the network names it

  Scenario: sharing a project mints a new seed
    Given workspace "kb" holds a private seed
    When the operator shares a project with another person
    Then a new seed is minted for the shared project
    And the private seed is not shared
    And the shared project's isotope is agreed explicitly, not derived from the private one

  Scenario: a private session publishes no mDNS record by default
    Given a session carries a private workspace
    And its policy does not permit discovery
    When lezz sea starts its hub
    Then the hub binds 127.0.0.1
    And the hub publishes no "_adhd-isotope._tcp" record
    And the app still learns the hub endpoint from the channel
    And a child adhd reaches the hub through "--prime-addr"

  Scenario: a permitted announcement carries the claim and not the content
    Given a private workspace whose policy permits discovery
    When its task is announced
    Then the announcement names the task id, the claimant and the tick
    And the announcement does not carry the task's content
    And the log holds the same claim and no payload

  Scenario: a public user verifies a private rotation as a mirror
    Given the public tree holds "package-lock.json"
    And a dev has applied a merge commit to the advisory tree
    Then a public user can check the lock file's sha under the merge reversal
    And the check agrees with the announcement
    And the public user learns nothing about the advisory's content

  Scenario: a guest outside the workspace cannot compute its task
    Given workspace "kb/security" is private and "kb" is public
    And a guest holds only the public tree
    Then the guest sees no advisory route
    And the guest holds no seed for the advisory task
    And the guest cannot compute the advisory task's isotope
