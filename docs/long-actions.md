# Long actions

One rule for anything in the panel that takes more than a moment.

## The panel's work runs in the background

Snapshots, restores, copies, moves, deletes, imports once the archive is in,
and making a server from a snapshot are operations. The panel owns them: the
request that starts one returns at once, and the operation shows on the
server's page and its row in the list, with its stage and how far along it is.
A toast says when it ends; one that failed also says so on the server's page.
While one runs, the server's actions it holds back are disabled the same way
everywhere.

## Only sending a file waits on the browser

A file sent from this computer needs the browser to stay. Two cases:

- **A new server's files** (an archive to import, content and a resource pack
  to upload, a picture). The form makes the server and goes straight to its
  page. The files go on being sent from a card in the corner, which stays
  while you move around the panel. Closing or reloading the tab would stop
  them, so the browser asks first. The card says how far it is, can stop the
  upload, and says what happened if it did not finish, with a link to where to
  put it right. When everything is in, the server is started, and a toast says
  so.
- **An upload on a page** (content, a resource pack, files in the file
  explorer). It shows how far it is, can be cancelled, and says that leaving
  the page stops it.

## Never just stop spinning

Every wait ends by saying what happened: a toast, the card in the corner, or
the server's page. A page never goes back to idle without saying.

## Tests

Each long action has a browser test that leaves the page midway and checks
that the action finishes, or says plainly that it stopped.
