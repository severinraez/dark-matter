dark matter is an assistant for llms to record information about a project alongside the code.
the model:

* each branch B has it's llm info companion B-llm
* when merging branches, the -llm variants are also merged. this can be done deterministically without merge conflicts (or with a file that deterministically solves them).
* when branching, the -llm branches also branch off the respective base -llm branch
* dark matter maintains a "shadow file tree" - the -llm branch starts out empty. it mirrors the directory structure of the code, but contains only llm notes

llms can interact with dark matter as follows

* the cli accepts multiple commands via stdin (to save tokens), separated by newline

cat | dm <<EOF
r:file1.rb
r:test/file2.rb
a:test/file3.rb:some notes\
about file3.rb
EOF

output:

file1.rb:<saved info>
file2.rb:<saved info>

# querying

the `r` command will not only notes about a file, but also related information which can be progressively discovered by the llm by follow-up invocations. it surfaces how much more information there is available to query

discovery dimensions:

* long notes for a file are not shown in full at first
* links to other notes are not expanded
* notes on parent files are not expanded

# verification

* `dm` first parses it's entire input
* if it's not syntactically correct, an error is shown
* if it is syntactically correct, successful commands are consumed, commands which error display their errors

# techstack

`dm` is a go binary

# testing

