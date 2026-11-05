---
name: dreadnought-patch-notes
description: Write player-facing patch notes for the Dreadnought private server, ready to paste into Discord, reported per game mode and built from the commits' "Patch notes:" blocks. Use whenever the operator asks for patch notes, release notes, a changelog for players, or "what changed" for the community, after changes to mmogbrain, the battle-server mod or the launcher. Also defines the per-game-mode "Patch notes:" block every commit body ends with.
---

# Patch notes for players

The operator pastes these into the community Discord. Players read them, not
developers. They want to know what plays differently, not how it was fixed.

## Gather the changes

1. Find where the last notes stopped. Ask nothing: use the conversation (the
   last notes given), else `git log --since=<date of last notes>`.
2. Read every commit since then, in **both** repositories (server and
   `../Dreadnought-Revival-Launcher`):
   `git log --format='%h %ad %s%n%b' --date=short --since=<date>`.
3. **Start from each commit's `Patch notes:` block** (the default since
   2026-11-05, see `CONTRIBUTING.md`): one line per player-visible change,
   already prefixed with its game mode and written for players. Sort those
   lines into the per-mode sections below, merge lines that describe the
   same change, and keep the newest wording when a later commit corrects an
   earlier one. Skip commits that say `Patch notes: none (internal)`.
4. A commit without the block (older ones) is read from its subject and body,
   and its changes are assigned to game modes the same way.
5. The rest of each body says what was verified and what was not; use it for
   the "please report" section.
6. Only list what is **deployed**. A change that is committed but not live
   goes under nothing, or is held back until it is deployed. Check the deploy
   time against the commit (memory files and the deploy script output record
   it). Launcher changes count as deployed once their release is published.

## Format

Copy this shape exactly: same heading style, same order, same tone.

```
**Patch notes – <day> <Month>**

**<Game mode or area>**
- <What a player will notice, in one or two plain sentences.> Before, <what
  was wrong>.
- ...

**<Next mode>**
- ...

**Known issues / please report**
- <Open problem, in player words.> We're investigating.
- The <X> changes are brand new. Please tell us whether <concrete thing to
  look for>, and whether <another concrete thing>.
```

- **Report per game mode.** One section per game mode that changed, using the
  in-game names: Team Deathmatch, Team Elimination, Conquest, Onslaught, Ceres
  Awakens, Proving Grounds, Havoc, Turbo TDM, Escalation. Game modes first, in
  that order; then **All game modes** for changes that affect every mode (a
  player reads their own mode's section and this one); then Launcher,
  Hangar, Market, Tech tree, Social, General. A mode with no change gets no
  section. A change that plays differently in several modes is listed under
  each of them, in that mode's terms.
- Map names as players know them: Ryugu Haven (Space02), Elysium (Paradise),
  Amirani, Juno (Space03), Cronian Rapids (Space04), Thebe (DansMap). Never the
  internal path.
- Bold a map or feature name when it is new or back.
- "Before, ..." sentences are good: players remember the old behaviour.
- End with **Known issues / please report**. Always ask players to confirm
  the things that are not yet verified live, naming exactly what to look
  for. That is how the operator gets test results.

## Rules

- **No internals.** No RVAs, offsets, function names, file names, switch
  files, log lines, sha sums or commit hashes. "The AI's ability tier was set
  too late" becomes "Command Ships use their abilities again".
- **Never invent.** Describe only what the commit actually changed. If a value
  is our estimate (a GUESS in the code), say so plainly: "The exact amounts
  are our best estimate, because the original values were never published."
- **Don't overclaim.** If it is only smoke-tested, the change still goes in the
  notes. Add it to "please report" too.
- **No sensitive data:** no domain, IPs, player names or IDs, emails or
  passwords. Players may be credited only if the operator asks, by the name
  they use in Discord.
- Leave out purely internal work: diagnostics, tests, refactors, tooling,
  log noise, and checks that found nothing to change. Exception: a player-reported
  problem that turned out to be fine. Say so in one line: "Checked: X already
  works like the original".
- Keep it short: one or two lines per bullet, no paragraphs.

## The `Patch notes:` block in commits

Every commit in both repositories ends its body (before the trailers) with
the block these notes are built from, in the same per-mode terms:

```
Patch notes:
- Conquest: Captured points now show as your team's territory on the minimap.
- All game modes: Match rewards are paid as soon as the end screen opens.
- Launcher: Shows when a new version is available and updates itself.
```

Same rules as the notes themselves (player words, no internals, no sensitive
data, never invent); `Patch notes: none (internal)` for work players never
see. Full rules: `CONTRIBUTING.md`, "The `Patch notes:` block".

## Deliver

Give the notes in the reply between `---` rules, ready to paste. Then offer
in one line to publish them as a shareable page.
