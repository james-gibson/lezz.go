@lezz
@z2-relational
@domain-le-roy
Feature: le.roy rotates or yields, and accepts nothing else
  le.roy is a one-shot tool over a workspace's rotation: it binds the commit,
  re-runs every verifier, and either advances the claim or hands it to another
  claimant. It accepts the challenge, never the answer: a rotation document that
  asserts its own pass is discarded and the verdict is recomputed. Every other
  request is refused by name, so a bypass has to be expressed as a verb someone
  can see.

  Scenario: le.roy rotates a clean workspace
    Given a clean workspace at a commit
    And every verifier passes in it
    When I run "le.roy rotate --claimant agent-a"
    Then it exits 0
    And the rotation names the same subject as the workspace HEAD
    And the tick is the next one
    And the claimant is "agent-a"

  Scenario: le.roy refuses to rotate a broken workspace
    Given a workspace with a lint failure
    When I run "le.roy rotate --claimant agent-a"
    Then it exits 2
    And the rotation is inverted
    And the refusal names "lint"
    And no rotation is printed as accepted

  Scenario: le.roy does not bind a dirty workspace
    Given a workspace with an uncommitted change
    When I run "le.roy rotate --claimant agent-a"
    Then it exits 2
    And the refusal says the verdict is unbound
    And it does not name a commit

  Scenario: le.roy yields the evidence, not a summary
    Given a clean workspace a rotation was taken over
    When I run "le.roy yield --to agent-b"
    Then it exits 0
    And the claimant is "agent-b"
    And every verifier is unchanged
    And every invariant keeps its subject

  Scenario: le.roy refuses a yield with nowhere to go
    Given a clean workspace
    When I run "le.roy yield --to agent-a --claimant agent-a"
    Then it exits 2
    And the refusal says the claimant already holds the work

  Scenario: le.roy accepts no other challenge
    When I run "le.roy assert --correct"
    Then it exits 2
    And it prints "le.roy accepts the rotation challenge only"

  Scenario: an asserted pass is not a verdict
    Given a rotation document that claims "pass" true with no verifier
    And a workspace whose lint fails
    When I run "le.roy check"
    Then the assertion is discarded
    And the verdict is recomputed from the workspace
    And the recomputed verdict names "lint"

  Scenario: le.roy jenkins always yields
    Given a clean workspace at a commit
    When I run "le.roy jenkins --to agent-b"
    Then it exits 0
    And the claimant is "agent-b"
    And it says the rotation was not checked
    And it does not claim the invariants hold

  Scenario: le.roy jenkins yields a broken workspace too
    Given a workspace with a lint failure
    When I run "le.roy jenkins --to agent-b"
    Then it exits 0
    And the claimant is "agent-b"
    And the rotation is marked unchecked
    And it does not report the rotation as correct

  Scenario: le.roy jenkins announces the yield on the network
    Given a clean workspace at a commit
    And an mDNS responder is reachable
    When I run "le.roy jenkins --to agent-b"
    Then it publishes a "_le-roy-yield._tcp" record
    And the record names the claimant "agent-b", the tick and a short subject
    And the record advertises a loopback address
    And it serves nothing on that address
    And the record carries no seed and no task content

  Scenario: a failed announcement does not fail the yield
    Given a clean workspace at a commit
    And mDNS is unavailable
    When I run "le.roy jenkins --to agent-b"
    Then it exits 0
    And the output says nothing was announced
    And the rotation is still yielded to "agent-b"

  Scenario: a receiver refuses an unchecked yield
    Given a yield that says the rotation was not checked
    When the receiver runs "le.roy check" on it
    Then it recomputes the verdict from the workspace
    And it accepts the rotation only if every verifier passes
