# Installer smoke checklist

MSI and MSIX installs cannot run in CI: command construction, log reading and
the install pipeline are covered by tests against a fake platform, and the NSIS
path runs against the real system in the Windows CI job. Run this checklist by
hand on a Windows PC before releasing a change to installer handling. Use a
throwaway Windows profile or a virtual machine, and start each case from a
build run with `-data-dir` so it touches nothing of the installed copy.

For every case, check afterwards: the Activity page has an entry for it, the
drawer shows the right buttons, and `%LOCALAPPDATA%\release-sync\logs` holds
nothing that should not be there.

## Per-user MSI

1. Track a repository that publishes an MSI that can install for the current
   user, with "Install apps for everyone on this PC" off.
2. Press Install. With "Silent installs" on, no installer window appears, and
   Windows shows an administrator prompt (the install is started elevated).
3. Expect: the app is in Installed apps, Launch starts it, "Installed in" shows
   its folder, and the MSI log is gone from the logs folder.
4. Press Uninstall, confirm. Expect the entry to leave Installed apps and the
   Item to read "Not installed".

## All-users MSI, elevation declined and accepted

1. Turn on "Install apps for everyone on this PC" (or the drawer's "Install for
   everyone") and press Install.
2. Decline the administrator prompt. Expect the toast "Installation was
   cancelled.", Activity "Canceled installing ...", no error in the drawer, and
   the Item still "Not installed".
3. Press Install again and accept. Expect the entry under "all users" in
   Installed apps (it has `ALLUSERS=1`).
4. Publish or pick an older release and update: the update keeps the scope of
   the installed copy even with the setting turned off.

## A failing MSI

1. Install an MSI that cannot succeed here, for example one that needs an
   administrator while declining is not possible, one whose product is already
   installed, or a corrupt package.
2. Expect the drawer to explain the failure in plain words (the installer's own
   message when its log has one, otherwise a cause such as "This installer needs
   an administrator account."), ending with "Details:" and the log path.
3. Expect the log to still be there and to open in a text editor.
4. For an MSI that asks for a license property, expect the question "Accept the
   license of ...?". Cancel: nothing is added and the install is cancelled.
   Accept: the property appears under "Custom installer arguments" and the
   installer runs again.

## Setup program

1. Install an NSIS or Inno Setup program. Expect no installer window. Install
   one of neither kind, with "setup" in its file name. Expect its own window.
2. Give an Item "Custom installer arguments" and confirm they replace the
   silent switches.
3. Choose an "Install folder", install, and confirm the program went there, or
   that a program that ignores the request is still recorded where it went.
4. Update with the program running: expect "... is running", "Close it and
   update" closing it, and "Open it again afterwards" starting it again.
5. Uninstall: expect the program's own uninstaller to run and the entry to
   leave Installed apps, or the message "The uninstaller finished, but the
   program still appears installed. It may have been cancelled." when it is
   cancelled in the uninstaller's window.

## MSIX

1. Install an MSIX (or .msixbundle). It may need its certificate trusted or
   developer mode on; a failure should show the system's reason.
2. Expect the drawer to read "Open it from the Start menu." with no Launch or
   See file location button.
3. Uninstall from this PC. Expect the app to leave the Start menu and
   `Get-AppxPackage` for its name.

## Windows Security scan and moving a portable app

These steps need a real Windows machine: the scanner and the shortcut files are
not available to the tests that run elsewhere.

1. With "Scan downloads with Windows Security" on, download the EICAR test file
   (a harmless string every antivirus flags) as a release file of a test
   repository. Expect "Windows Security says this download is dangerous (...)"
   and the file gone, whether Windows removed it first or the scan found it.
2. Download an ordinary file. Expect "Windows Security: clean" in its summary.
   Turn Windows Security real-time protection off and another antivirus on, or
   rename `MpCmdRun.exe` away: expect "Windows Security: couldn't scan" and the
   download to go on.
3. Install a portable app with a Start menu shortcut. Pin its program to the
   taskbar and copy its shortcut to the Desktop. Use "Move to another folder…"
   and expect all three shortcuts to open the app from the new folder, with
   their icons intact.
4. Move an app whose program is running: expect "Close it and move".
