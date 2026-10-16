export class PageLoadError extends Error {
  constructor(cause: unknown) {
    super("Unable to load this page. Reload the window to try again.", { cause });
    this.name = "PageLoadError";
  }
}

/** Keep import failures distinct from errors thrown by the loaded component. */
export function loadPage<Module>(module: Promise<Module>): Promise<Module> {
  return module.catch((cause: unknown) => {
    throw new PageLoadError(cause);
  });
}

export function reloadPageWindow() {
  // A tab remount cannot reset React.lazy's cached rejected promise.
  window.location.reload();
}
