High-severity defects affect DNS recovery, API authorization, and first-run UI functionality.

I read all 36 production Go files, the frontend, tests, packaging scripts, and relevant pinned dependency sources. No files were modified. JavaScript checks reproduced several frontend failures in memory. Go/race tests and rendered UI checks were unavailable because the command runner failed and no browser surface was available.

1. **High — Fresh configuration breaks frontend initialization.** [config.go:106](D:/AI/DoH/internal/config/config.go:106), [app.js:316](D:/AI/DoH/frontend/app.js:316), [app.js:437](D:/AI/DoH/frontend/app.js:437).  
   **Scenario:** Fresh installation returns `rules: null`; rendering calls `.some()`, `.map()`, and `.filter()` on it. If the service is running at GUI launch, the rejected initial refresh also prevents polling timers from starting. Reproduced in memory.  
   **Fix:** Serialize empty collections as `[]` and normalize nullable arrays when receiving configuration.

2. **High — Failed DNS restoration still stops the resolver and reports successful disable.** [daemon.go:251](D:/AI/DoH/internal/daemon/daemon.go:251), [daemon.go:139](D:/AI/DoH/internal/daemon/daemon.go:139).  
   **Scenario:** `netsh`, `resolvectl`, or `networksetup` fails during restore. The daemon nevertheless stops its DNS listeners, marks itself inactive, and persists `enabled=false`. Interfaces still pointing at loopback lose DNS; inactive maintenance never retries restoration.  
   **Fix:** Return restoration errors, retain a recovery state, and keep the listener available while affected interfaces remain redirected.

3. **High — Uninstall deletes the recovery executable despite restoration failure.** [svc.go:188](D:/AI/DoH/internal/svc/svc.go:188).  
   **Scenario:** The uninstall safety-net restore fails. The error is only logged; uninstall removes the token, ownership information, and binary and can return success. The GUI then says “системный DNS восстановлен” although DNS remains redirected.  
   **Fix:** Make successful restoration a prerequisite for completing uninstall; preserve recovery tooling and return the error.

4. **High — Uninstall after a crash leaves managed hosts entries behind.** [svc.go:177](D:/AI/DoH/internal/svc/svc.go:177), [hosts.go:75](D:/AI/DoH/internal/hosts/hosts.go:75).  
   **Scenario:** Kill the service in hosts mode, then uninstall while it is stopped. No daemon cleanup runs, and uninstall only restores `sysdns`; managed hosts entries continue overriding domains after the application is removed.  
   **Fix:** Explicitly remove the managed hosts block during uninstall, including when the service was already dead, and propagate cleanup failures.

5. **High — Failed startup after a crash can leave DNS permanently redirected.** [daemon.go:84](D:/AI/DoH/internal/daemon/daemon.go:84), [daemon.go:101](D:/AI/DoH/internal/daemon/daemon.go:101).  
   **Scenario:** DNS was redirected when the process died; persisted configuration remains enabled. On restart, another process occupies a required DNS port. `enableLocked()` fails, but startup only logs the error and returns success without restoring the backup or scheduling another enable attempt.  
   **Fix:** Restore redirected DNS whenever startup cannot establish the required resolver, or maintain an explicit recovery/retry state.

6. **High — macOS restoration discards backups for disabled or renamed services.** [sysdns_darwin.go:27](D:/AI/DoH/internal/sysdns/sysdns_darwin.go:27), [sysdns_darwin.go:88](D:/AI/DoH/internal/sysdns/sysdns_darwin.go:88), [sysdns.go:153](D:/AI/DoH/internal/sysdns/sysdns.go:153).  
   **Scenario:** Disable or rename Wi-Fi after applying loopback DNS. Restoration treats the original service name as absent, skips it, returns success, and deletes the backup. Re-enabling the service leaves persistent loopback DNS. A failed service enumeration also incorrectly produces successful restoration.  
   **Fix:** Track stable service identities, restore disabled services, propagate enumeration failures, and retain unresolved backup entries.

7. **High — Hosts writes can destroy unrelated system entries on interruption.** [hosts.go:89](D:/AI/DoH/internal/hosts/hosts.go:89).  
   **Scenario:** `os.WriteFile` truncates the existing hosts file; kill the process or encounter a write failure before completion. User-managed entries are lost, and there is no durable original-content backup.  
   **Fix:** Journal original content durably before in-place writes; use atomic replacement with preserved metadata where supported and provide recovery for bind mounts/ACL-sensitive files.

8. **High — Linux file restoration has a destructive crash window.** [sysdns_linux.go:242](D:/AI/DoH/internal/sysdns/sysdns_linux.go:242), [sysdns_linux.go:199](D:/AI/DoH/internal/sysdns/sysdns_linux.go:199).  
   **Scenario:** Restoration removes `/etc/resolv.conf`, then the process dies before recreating it. Since disabling is persisted afterward, restart can still see `enabled=true`; `ensureFile()` sees the missing file and overwrites the valid original backup with `Missing=true`.  
   **Fix:** Restore through an atomic temporary-file/symlink replacement and preserve the original journal across interrupted restore operations.

9. **High — Backup persistence does not establish the claimed power-loss guarantee.** [sysdns.go:108](D:/AI/DoH/internal/sysdns/sysdns.go:108).  
   **Scenario:** The temporary backup is synced, renamed, and DNS is changed, but power fails before the directory entry is durable. The expected backup pathname can be missing after reboot. File `fsync` alone does not guarantee directory-entry persistence. [Linux `fsync` documentation](https://man7.org/linux/man-pages/man2/fsync.2.html).  
   **Fix:** Sync the containing directory after rename on Unix and use an appropriate durable replacement sequence on Windows.

10. **High — Unknown or malformed ownership fails open.** [perm_unix.go:15](D:/AI/DoH/internal/api/perm_unix.go:15), [perm_windows.go:15](D:/AI/DoH/internal/api/perm_windows.go:15).  
    **Scenario:** Install directly as root/administrator without an owner, or lose the owner file. Unix makes the API token world-readable; Windows leaves inherited read access. Any local user can then change machine-wide DNS through the privileged API.  
    **Fix:** Default to administrator-only access and reject malformed ownership instead of widening authorization.

11. **High — Windows briefly exposes each new token before applying its restrictive ACL.** [api.go:49](D:/AI/DoH/internal/api/api.go:49), [perm_windows.go:29](D:/AI/DoH/internal/api/perm_windows.go:29).  
    **Scenario:** Another local user watches token creation and reads it between `WriteFile` and `SetNamedSecurityInfo`. The Unix-style `0600` creation mode does not establish the intended Windows DACL; the inherited ACL initially applies. [Windows file-security documentation](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights).  
    **Fix:** Create the file with its restrictive security descriptor already installed, then publish it atomically.

12. **High — HTTPS validation is bypassed by HTTP redirects.** [upstream.go:101](D:/AI/DoH/internal/doh/upstream.go:101), [upstream.go:251](D:/AI/DoH/internal/doh/upstream.go:251).  
    **Scenario:** A configured HTTPS endpoint returns a `307` redirect to HTTP. The default Go redirect policy follows it and resends the DNS POST body in plaintext.  
    **Fix:** Implement `CheckRedirect` that requires HTTPS and enforces an explicit policy for destination hosts.

13. **High — Control API bind failure leaves a running, uncontrollable daemon.** [svc.go:53](D:/AI/DoH/internal/svc/svc.go:53).  
    **Scenario:** Another local process occupies `127.0.0.1:47153` before startup. The daemon can already have redirected DNS, while API startup merely logs its bind failure. SCM/systemd/launchd sees successful service startup, so recovery is not triggered.  
    **Fix:** Establish the API listener synchronously before reporting startup success; unwind DNS changes on failure.

14. **High — Windows IPv4-only listening leaves external IPv6 DNS available.** [sysdns_windows.go:170](D:/AI/DoH/internal/sysdns/sysdns_windows.go:170).  
    **Scenario:** Configure only `127.0.0.1:53` on an adapter with existing IPv6 DNS servers. The IPv6 configuration is untouched; queries that Windows sends to those servers bypass DoH and domain rules.  
    **Fix:** Ensure all usable DNS-server families are redirected or explicitly disabled/restored; do not silently leave an external family configured.

15. **High — uncertain on default Windows ACLs: existing state directories are trusted without hardening.** [owner.go:24](D:/AI/DoH/internal/paths/owner.go:24), [sysdns.go:92](D:/AI/DoH/internal/sysdns/sysdns.go:92).  
    **Scenario:** A pre-existing state directory is writable by another user. `MkdirAll` preserves its permissions, allowing that user to replace ownership/configuration/backup files subsequently trusted by the privileged service. The conditional code defect is clear; default-directory exploitability was not inspected on this machine.  
    **Fix:** Establish and verify administrator-controlled directory ownership/DACLs and reject unsafe reparse points or writable existing files.

16. **Medium — Old in-flight queries survive routing changes and repopulate the new cache.** [router.go:126](D:/AI/DoH/internal/resolver/router.go:126), [router.go:300](D:/AI/DoH/internal/resolver/router.go:300), [router.go:356](D:/AI/DoH/internal/resolver/router.go:356).  
    **Scenario:** Provider A is resolving a domain when configuration switches it to B. After the cache reset, A’s response populates the shared cache; new requests can also join A’s existing singleflight call.  
    **Fix:** Include a configuration generation in cache/flight identities and reject cache writes from obsolete generations. Apply the same protection to explicit flushes.

17. **Medium — Configuration changes are not transactional with operational changes.** [daemon.go:146](D:/AI/DoH/internal/daemon/daemon.go:146), [daemon.go:182](D:/AI/DoH/internal/daemon/daemon.go:182).  
    **Scenario:** Enabling succeeds but saving fails, leaving an active daemon with persisted disabled state. Conversely, a listener change is saved before restart; a bind failure returns an error while leaving the new configuration persisted and the resolver inactive.  
    **Fix:** Implement explicit commit/rollback behavior for both persisted configuration and running state; return the effective state after partial failures.

18. **Medium — Removing every hosts rule leaves the old entries active.** [daemon.go:327](D:/AI/DoH/internal/daemon/daemon.go:327).  
    **Scenario:** In active hosts mode, delete or disable all rules. Configuration is saved, but `writeHostsLocked()` rejects the empty result before removing the existing block. Previously configured domains remain overridden.  
    **Fix:** Treat an empty enabled-rule set as a valid request to remove managed entries; distinguish it from failed resolution of nonempty rules.

19. **Medium — Linux DNS equality prevents repair of routing domains.** [sysdns_linux.go:146](D:/AI/DoH/internal/sysdns/sysdns_linux.go:146).  
    **Scenario:** Setting link DNS succeeds but setting `~.` fails, or a network manager later changes only domains. Subsequent maintenance sees matching DNS servers and skips the domain operation entirely, potentially clearing the earlier error without repairing it.  
    **Fix:** Compare and repair DNS servers and routing/search domains independently.

20. **Medium — Linux file mode ignores changed target servers.** [sysdns_linux.go:186](D:/AI/DoH/internal/sysdns/sysdns_linux.go:186).  
    **Scenario:** A managed file already points at one listener, while the current configuration/server uses another address. The marker alone causes `ensureFile()` to return success, retaining stale nameservers.  
    **Fix:** Parse and compare the managed nameserver list, while retaining the original unmanaged backup.

21. **Medium — Linux file mode removes resolver search configuration.** [sysdns_linux.go:203](D:/AI/DoH/internal/sysdns/sysdns_linux.go:203).  
    **Scenario:** Original configuration contains `search example.net`; a short-name lookup for `portal` previously resolved `portal.example.net`. Enabling file mode removes the search directive, so the lookup now asks for the wrong name.  
    **Fix:** Preserve applicable `search`, `domain`, and resolver options while replacing nameservers.

22. **Medium — Windows restoration suppresses errors for disconnected adapters.** [sysdns_windows.go:189](D:/AI/DoH/internal/sysdns/sysdns_windows.go:189).  
    **Scenario:** An adapter is disconnected during restore and its registry write fails. IPv4 errors are ignored when it is absent, and IPv6 registry errors are always ignored. Restoration can delete the backup while that adapter retains loopback DNS.  
    **Fix:** Propagate registry failures for existing adapter keys and retain unresolved entries; distinguish removed adapters from disconnected ones.

23. **Medium — Emergency restore is undone by a running service.** [svc.go:231](D:/AI/DoH/internal/svc/svc.go:231), [daemon.go:369](D:/AI/DoH/internal/daemon/daemon.go:369).  
    **Scenario:** Run `restore-dns` while the daemon remains active. The command restores DNS and deletes its backup, but maintenance redirects DNS again within approximately 20 seconds.  
    **Fix:** Coordinate emergency restoration with disabling/stopping the daemon and prevent concurrent reapplication.

24. **Medium — Shutdown work is unbounded and insufficiently cancellable.** [sysdns.go:184](D:/AI/DoH/internal/sysdns/sysdns.go:184), [daemon.go:298](D:/AI/DoH/internal/daemon/daemon.go:298), [daemon.go:119](D:/AI/DoH/internal/daemon/daemon.go:119).  
    **Scenario:** A system command hangs, or many hosts rules each require a ten-second timeout. Commands have no deadline, and hosts work uses background contexts rather than daemon cancellation. Stop can wait beyond the service manager’s shutdown limit, causing termination before restoration.  
    **Fix:** Bound external commands, propagate daemon/request cancellation, and give recovery work a controlled shutdown deadline.

25. **Medium — Cache/singleflight identity omits answer-affecting request state.** [cache.go:37](D:/AI/DoH/internal/resolver/cache.go:37), [upstream.go:227](D:/AI/DoH/internal/doh/upstream.go:227).  
    **Scenario:** Two clients send the same question with different ECS subnets. Their forwarded queries share one key and therefore one answer. RD differences and EDNS versions also collide; an unsupported-version request can receive a cached successful response.  
    **Fix:** Normalize requests before forwarding and caching; bypass or separately key requests with answer-affecting options/flags.

26. **Medium — Cache policy serves records beyond their authoritative lifetime.** [cache.go:102](D:/AI/DoH/internal/resolver/cache.go:102), [cache.go:79](D:/AI/DoH/internal/resolver/cache.go:79).  
    **Scenario:** With default `MinTTL=30`, a TTL-zero or five-second answer remains cached for 30 seconds; expired records are returned with TTL 1. Shorter-lived authority/additional records can similarly outlive their original TTL.  
    **Fix:** Never extend TTL-zero records or silently resurrect expired records; expire required RRsets and discard expired optional records.

27. **Medium — CNAME-bearing negative answers use the wrong cache lifetime.** [cache.go:98](D:/AI/DoH/internal/resolver/cache.go:98), [cache.go:138](D:/AI/DoH/internal/resolver/cache.go:138).  
    **Scenario:** NXDOMAIN contains a CNAME with TTL 600 and an SOA negative lifetime of 60. Because the answer section is nonempty, the response is treated as positive and can remain cached for 600 seconds. Negative responses without SOA also receive an invented lifetime.  
    **Fix:** Detect negative answers independently of answer-section emptiness and derive their lifetime from SOA, bounded by the CNAME chain. [RFC 2308](https://datatracker.ietf.org/doc/html/rfc2308#section-5).

28. **Medium — DoH HTTP `Age` is ignored.** [upstream.go:269](D:/AI/DoH/internal/doh/upstream.go:269).  
    **Scenario:** An HTTP cache returns a DNS RRset with TTL 600 and `Age: 250`; the client forwards/caches it for another 600 seconds instead of 350.  
    **Fix:** Subtract HTTP age, saturating at zero, before forwarding or caching records. [RFC 8484 §5.1](https://www.rfc-editor.org/info/rfc8484/#section-5.1).

29. **Medium — Bootstrap fallback stops at TCP success, before TLS success.** [upstream.go:190](D:/AI/DoH/internal/doh/upstream.go:190).  
    **Scenario:** The first bootstrap address accepts TCP but hangs during TLS or presents an invalid certificate; another address would work. The race already selected the first address and recorded it as `lastOK`, so the provider repeatedly fails without trying the healthy TLS endpoint.  
    **Fix:** Include TLS establishment in address selection or retry remaining addresses after TLS failure.

30. **Medium — Singleflight ignores waiting callers’ cancellation.** [router.go:300](D:/AI/DoH/internal/resolver/router.go:300).  
    **Scenario:** A caller with a short deadline joins a slower existing query and blocks until that query finishes. Conversely, cancellation of the first caller aborts the shared operation for later callers whose deadlines remain valid.  
    **Fix:** Use a bounded shared-operation context and `DoChan` with per-caller cancellation.

31. **Medium — Rapid frontend edits overwrite one another.** [app.js:215](D:/AI/DoH/frontend/app.js:215).  
    **Scenario:** Add two presets or toggle two providers before the first save returns. Both mutations clone the same configuration; the second full-config write discards the first change. Reproduced in memory.  
    **Fix:** Serialize mutations against the latest accepted configuration, preferably with server-side revision checking.

32. **Medium — GUI retains the previous service session after restart.** [app.js:146](D:/AI/DoH/frontend/app.js:146), [app.js:76](D:/AI/DoH/frontend/app.js:76), [log.go:40](D:/AI/DoH/internal/resolver/log.go:40).  
    **Scenario:** Restart after sequence 1000. The new daemon starts its log at 1, but the GUI continues requesting entries after 1000, suppressing the journal/map until the counter catches up. Cached configuration also survives reconnection and can overwrite newer service configuration.  
    **Fix:** Expose a service-instance identifier and reset log cursors, local history, and configuration on instance changes.

33. **Medium — Missing/no-access states leave configuration controls operable.** [app.js:65](D:/AI/DoH/frontend/app.js:65), [app.js:297](D:/AI/DoH/frontend/app.js:297), [app.js:361](D:/AI/DoH/frontend/app.js:361).  
    **Scenario:** Open Providers or Rules before service installation and submit a form. Handlers dereference `S.cfg=null` outside error handling, producing an exception without useful feedback. After losing access, stale controls remain editable.  
    **Fix:** Disable service-dependent forms until authorized configuration is loaded and guard every mutation entry point.

34. **Medium — Configuration rendering destroys keyboard focus.** [app.js:223](D:/AI/DoH/frontend/app.js:223), [app.js:264](D:/AI/DoH/frontend/app.js:264), [app.js:330](D:/AI/DoH/frontend/app.js:330).  
    **Scenario:** A keyboard user moves a provider earlier in a chain. Saving rebuilds the chain, provider rows, and rule rows, removing the focused button; focus falls back to the document.  
    **Fix:** Update elements in place or restore focus to the corresponding control after rendering.

35. **Medium — Light-theme accent text and focus indicators have insufficient contrast.** [style.css:36](D:/AI/DoH/frontend/style.css:36), [style.css:51](D:/AI/DoH/frontend/style.css:51), [style.css:242](D:/AI/DoH/frontend/style.css:242).  
    **Scenario:** Light mode renders proxy verdicts/tags in yellow on white. Calculated contrast is approximately **1.82:1**, and approximately **1.62:1** on the page background. The yellow focus outline has the same weak contrast.  
    **Fix:** Use darker semantic text/focus colors while retaining yellow for filled controls. [WCAG contrast guidance](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html).

36. **Medium — Changing reduced-motion preference can create unbounded SVG debris.** [app.js:12](D:/AI/DoH/frontend/app.js:12), [map.js:220](D:/AI/DoH/frontend/map.js:220), [style.css:250](D:/AI/DoH/frontend/style.css:250).  
    **Scenario:** Launch with motion enabled, then enable reduced motion while queries continue. JavaScript retains `reduced=false`, but CSS disables burst animations. Their `animationend` cleanup never runs, so burst circles accumulate despite the packet cap.  
    **Fix:** Subscribe to media-query changes, stop/clear animation state, and make element cleanup independent of animation events.

37. **Medium — Russian route labels claim proxy use without evidence.** [app.js:172](D:/AI/DoH/frontend/app.js:172), [map.js:172](D:/AI/DoH/frontend/map.js:172), [daemon.go:455](D:/AI/DoH/internal/daemon/daemon.go:455).  
    **Scenario:** Route a rule through Cloudflare; an ordinary address is still labeled “через прокси” and animated toward “за рубежом”. Diagnostics additionally treat any address in a learned `/24` or `/64` as a proxy, potentially misclassifying another tenant or CDN address.  
    **Fix:** Label rule routing as “по правилу”; present proxy/geography classification as a heuristic unless independently established.

38. **Medium — Release scripts compile Wails development behavior.** [build-windows.ps1:10](D:/AI/DoH/scripts/build-windows.ps1:10), [build-linux.sh:12](D:/AI/DoH/scripts/build-linux.sh:12), [build-macos.sh:7](D:/AI/DoH/scripts/build-macos.sh:7).  
    **Scenario:** Release binaries are built without `production`. In the pinned Wails version, `FRONTEND_DEVSERVER_URL` can substitute an external frontend, contradicting the assumption that the unsandboxed Linux webview only displays embedded assets. Development tooling also remains available.  
    **Fix:** Apply the Wails production build tag consistently on every target, including GTK3 builds.

39. **Medium — Pinned Windows service dependency can wait forever during upgrade/uninstall.** [svc.go:156](D:/AI/DoH/internal/svc/svc.go:156), [go.mod:7](D:/AI/DoH/go.mod:7), [service_windows.go:521](C:/Users/Archer/go/pkg/mod/github.com/kardianos/service@v1.3.0/service_windows.go:521).  
    **Scenario:** The service remains `StopPending`. The dependency’s timeout branch executes an unlabeled `break`, which exits the `select`, not the surrounding loop. Consequently, `s.Stop()` can block indefinitely before the application’s own wait logic runs.  
    **Fix:** Patch/update the dependency or implement bounded SCM stop handling; abort replacement when stopping fails.

40. **Low — Navigation loses its current-page ARIA state.** [app.js:44](D:/AI/DoH/frontend/app.js:44).  
    **Scenario:** After navigation, `toggleAttribute` sets `aria-current=""` instead of `"page"`. Assistive technology no longer receives a valid current-page indication.  
    **Fix:** Set `aria-current="page"` on the selected button and remove it from the others.

41. **Low — Provider health rows remain stale during polling.** [app.js:99](D:/AI/DoH/frontend/app.js:99), [app.js:269](D:/AI/DoH/frontend/app.js:269).  
    **Scenario:** Initial rendering occurs before health probes finish, or a provider subsequently fails. Refresh updates the map but does not refresh provider-row latency/availability; those values remain blank or outdated until a configuration mutation rebuilds them.  
    **Fix:** Update health cells from each status refresh without rebuilding focused controls.

42. **Low — Responsive SVG scaling makes map labels unusually small.** [map.js:7](D:/AI/DoH/frontend/map.js:7), [style.css:138](D:/AI/DoH/frontend/style.css:138), [gui.go:238](D:/AI/DoH/gui.go:238).  
    **Scenario:** At the configured 1040-pixel window width, the map has approximately 714 pixels of drawing width for a 1000-unit viewBox. Nominal 14-unit labels become about 10 pixels; 12-unit metadata becomes about 8.6 pixels.  
    **Fix:** Use a responsive diagram layout, larger labels, or a readable minimum diagram width with scrolling.

43. **Low — Log resizing loses concurrent entries.** [log.go:82](D:/AI/DoH/internal/resolver/log.go:82).  
    **Scenario:** A query is logged after `Resize()` takes its `After()` snapshot but before it reacquires the mutex. Replacing the buffer with that snapshot drops the newly added entry.  
    **Fix:** Snapshot and replace the ring buffer within one critical section.

44. **Low — DoH response matching omits question class.** [upstream.go:273](D:/AI/DoH/internal/doh/upstream.go:273).  
    **Scenario:** An upstream responds to a CH question with an IN question having the same name/type. Validation accepts it; routing overwrites the question with the client’s CH question and can cache the mismatched response under CH.  
    **Fix:** Validate QCLASS alongside QNAME, QTYPE, and the expected opcode.

Checked and found correct in the reviewed paths:

- Exact Host checking, random tokens, constant-time comparison, custom-header authorization, and request-body limits protect against ordinary rebinding/CSRF.
- System commands use argument arrays; Windows adapter identifiers are numeric. macOS elevation quotes both shell and AppleScript layers correctly.
- `SHELLEXECUTEINFOW` has the correct 112-byte layout for the targeted Windows architectures; elevation closes its process handle.
- TLS certificate verification remains enabled; bootstrap avoids recursion through system DNS. TCP-race losers are drained and closed.
- Rule matching respects label boundaries; rule sinkholes fall back; NXDOMAIN is final. Truncated responses are excluded from caching, and UDP replies honor client size limits.
- Frontend strings from normal service payloads are escaped or assigned through `textContent`; I found no confirmed direct XSS path.