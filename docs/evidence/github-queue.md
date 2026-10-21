# Evidence: a live `github-queue` merge

U11 verification, second item. The fixture tests in `internal/merge` prove the
`github-queue` mechanism against a fake GitHub. This file records one live merge
through GitHub's merge queue on an organisation repository that the operator
names.

The live run is an operator act. It needs a real repository, the loop App's
installation on that repository and a merge-queue rule on its default branch.
No agent can run it.

## What the live run must prove

1. GitHub accepts the App's hand-off: `enqueuePullRequest` with the App
   installation token and the reviewed head as `expectedHeadOid`.
2. GitHub checks `loop/eligible` on the merge-group commit, not on the PR head.
3. The sweep posts `loop/eligible` on the merge-group commit of a group whose
   every PR is eligible at its queued head. GitHub then merges the group.
4. The merge-group branch name has the form that `internal/merge/ghqueue/group.go`
   reads: `gh-readonly-queue/<base>/pr-<number>-<sha>`. The `<sha>` is the
   commit that the group builds on. For the first group, that commit is the
   base tip. For a later group, that commit is the commit of the group ahead
   of it. GitHub may change this format. The sweep posts nothing on a name it
   cannot read.
5. The reactor or the hand-off job writes `done` exactly once for each merged
   ticket.

## Procedure

The operator does these steps.

1. Name an organisation repository for the run. Install the loop App on that
   repository only.
2. On the default branch, add these rules: require a pull request, require the
   CI check, require `loop/eligible` from the loop App, and require a merge
   queue with the merge method set to merge commits. Set the group size to 2 or
   more, so that one group can hold two PRs.
3. Commit `.loop/config.yaml` with `merge: {mechanism: github-queue}` to the
   default branch.
4. Run `loop project add` for the repository. Expect it to pass. Remove the
   merge-queue rule, run the add again, and expect a refusal that names
   `merge_queue`. Put the rule back.
5. Let the loop take two tickets to `ready_to_merge` at the same time, so that
   one merge group holds both PRs.
6. Watch the hand-off job tick. Record the evidence below.

## Results

PENDING — operator run. Nothing below is recorded yet. Do not fill it from a
fixture.

Record each item with the command or the page that shows it.

- Date of the run, the loop release, and the repository (owner/name).
- The `loop project add` answer with the rule, and the refusal without it.
- For each PR: its number, its ticket, the reviewed head sha, and the
  `merge_tracking` row after the hand-off (state `handed`).
- The `enqueuePullRequest` answer for each PR, or the PR's merge-queue page
  that shows it queued.
- Every merge-group branch name and its commit sha, as
  `GET /repos/<owner>/<name>/git/matching-refs/heads/gh-readonly-queue/<base>/`
  returned it while the group ran. Note which sha each name holds. That proves
  or disproves item 4.
- The commit statuses of each merge-group commit, with the `loop/eligible`
  status and its creator (the loop App).
- The merge commit of each PR, and the `transitions` rows of each ticket with
  verb `merged` (exactly one for each ticket).
- Any step that did not behave as this file states, with GitHub's answer.
