@lab-safety
@ingress
@le-ad
@address
Feature: le.ad — the empty desk returns to empty when its lease runs out
  A seed-addressed connection point is allowed to hold an expectation, never
  content. The signed announcement lets a visitor expect a call; the desk says
  plainly whether the lease is live and, when it is not, becomes empty again.

  Provenance — sheets/ingress/le_ad_desk.sh:
    curl -fsS "$DESK_URL/desk" | jq -e '.empty == true and .content == 0'
    seed-announce -key "$ANNOUNCE_KEY" -seed "$SEED" -name wan-ingress -slot wan-ingress -rotation "$ROTATION" -nonce "$NONCE" -dns-domain "$DNS_DOMAIN"
    curl -fsS "$DESK_URL/desk" | jq -e '.empty == false and .content == 0 and .rotation == "$ROTATION"'
    sleep "$((2 * TICK_SECONDS + 1))"
    curl -fsS "$DESK_URL/desk" | jq -e '.empty == true and .content == 0'

  Scenario: an unarmed desk reports empty without content
    Given a running le.ad desk with no live signed rotation
    When I run "curl -fsS <desk>/desk"
    Then the command exits 0
    And the JSON field "empty" is true
    And the JSON field "content" is 0

  Scenario: a signed rotation occupies the desk without content
    Given a signed rotation announced to the desk for slot "wan-ingress"
    When I run "curl -fsS <desk>/desk"
    Then the command exits 0
    And the JSON field "empty" is false
    And the JSON field "content" is 0
    And the JSON field "rotation" is "<rotation>"

  Scenario: the desk returns to empty when the lease runs out
    Given a desk occupied by a signed rotation with a two-tick lease
    When I run "curl -fsS <desk>/desk" after the lease runs out
    Then the command exits 0
    And the JSON field "empty" is true
    And the JSON field "content" is 0
