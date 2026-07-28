Feature: Listing and Inspecting Knowledge Bubbles
  Devon runs `ox kb list` to see every Knowledge Bubble he can access — his
  personal notes, profile, and the team's shared bubbles — in one list. He can
  filter by type and inspect a bubble to see what it holds. Bubbles are
  Curator-maintained syntheses (ox ADR-028): team contexts and ledgers are
  separate, permanent conversation stores and never appear in this list —
  Devon reads those with `ox teams` and `ox status`.

  See also: business-actions/inspect-bubbles.md
  See also: team-context/team-ctx.feature

  Rule: Listing shows every bubble the coworker can access — and nothing else

    Scenario: Devon lists all his Knowledge Bubbles
      Given Devon is signed in and a member of "Acme Engineering"
      When he lists his Knowledge Bubbles
      Then ox shows the bubbles returned by the KB API in one list

    Scenario: Conversation stores never appear as bubbles
      Given Devon's team has team contexts and ledgers alongside its bubbles
      When Devon lists his Knowledge Bubbles
      Then ox shows only real Knowledge Bubbles
      And no team context or ledger is presented as a bubble

    Scenario: The KB API being unavailable yields an empty list, not synthesized rows
      Given the KB API is unavailable for Devon's account
      When Devon lists his Knowledge Bubbles
      Then ox shows an empty list
      And no legacy rows are synthesized to fill the gap

  Rule: Bubbles can be filtered by type

    Scenario Outline: Devon filters the bubble list by type
      Given Devon can access bubbles of several types
      When he lists bubbles filtered to "<type>"
      Then ox shows only the "<type>" bubbles

      Examples: Bubble types
        | type     |
        | personal |
        | profile  |
        | team     |
        | repo     |
        | custom   |

  Rule: A bubble can be inspected

    Scenario: Devon inspects what a team bubble holds
      Given Devon sees a team bubble in his list
      When he inspects that bubble
      Then ox shows him what the bubble holds
