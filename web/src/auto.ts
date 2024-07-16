import { Live } from "./live";
import { Hooks } from "./interop";
import {LiveEvent as LE} from "./event";

declare global {
    interface Window {
        Hooks: Hooks;
        Live: Live;
    }
    export const LiveEvent: typeof LE;
    export type LiveEvent = LE;
}
(window as any).LiveEvent = LE;

document.addEventListener("DOMContentLoaded", (_) => {
    if (window.Live !== undefined) {
        console.error("window.Live already defined");
    }
    const hooks = window.Hooks || {};
    window.Live = new Live(hooks);
    window.Live.init();
});
