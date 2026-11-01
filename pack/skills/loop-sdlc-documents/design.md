# <epic title>: design record

**Epic:** <epic id>
**PRD:** docs/prd/<slug>.md
**Prototype:** `loop/<epic id>-prototype` at `<40-character commit>`

> Copy this file to `docs/design/<epic id>/record.md`. Put the picture of each state in the same
> directory. Each line of this file has a fixed shape, because each reader of the record reads it
> with one parser.
>
> **`Epic:` is the name of the directory.** **`PRD:` is the path of the epic's PRD in the
> repository.** The gate runs in CI with no server, so it reads the stories from that file.
>
> **`Prototype:` pins one commit of the prototype branch.** The branch is
> `loop/<epic id>-prototype`. The commit is 40 hexadecimal characters. When the branch adds or
> changes the record, the gate checks that `refs/remotes/origin/loop/<epic id>-prototype` holds
> the commit.
>
> A screen is a `### S<n>. <name>` heading. Its `**Stories:**` line names each story of the PRD
> that the screen shows. Each `UI: yes` story of the PRD has at least one screen.
>
> A state is one list item under its screen. Its id is `S<n>.<m>`. It names a picture, a `.png`
> file in this directory, and a target, the string that the capture command receives. A state
> can end with a pixel mark. The mark `pixel` alone sets the default threshold of 0.5% of the
> pixels. The mark `pixel <n>%` sets a threshold from 0% to 5%. Write the `%` sign.
>
> The `design` gate of `loop gates` checks each line of this shape. Delete this block before you
> commit.

## Screens

### S1. <screen name>

**Stories:** US-01, US-03

- **S1.1 <state name>.** Picture `s1-1.png`, target `<target>`.
- **S1.2 <state name>.** Picture `s1-2.png`, target `<target>`, pixel.
- **S1.3 <state name>.** Picture `s1-3.png`, target `<target>`, pixel 1.5%.
