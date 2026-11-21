# Third-party notice

The curated repositories and topic queries of each category, the queries
behind each list, the noise filter, the ranking and movement of a list, the
request budgeting and the look-up of downloads follow the behaviour of
`DiscoverService` and `DiscoverCatalog` in the ReleaseSync project, which
carries this licence. The code was written anew in Go. Lists, rankings and
answers are kept in SQLite, searches are spaced by a token bucket the allowance
can read, and only definite answers about downloads are remembered. Self-hosted
Forgejo repositories are not offered, and the Command-line tools category is
new.

```text
MIT License

Copyright (c) 2026 ReleaseSync contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
