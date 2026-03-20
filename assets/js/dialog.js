(function () {
  "use strict";

  const LAYER_ID = "tui-dialog-layer-root";

  function getLayerRoot() {
    let root = document.getElementById(LAYER_ID);
    if (root) return root;

    root = document.createElement("div");
    root.id = LAYER_ID;
    root.setAttribute("data-tui-dialog-layer-root", "true");
    document.body.appendChild(root);
    return root;
  }

  function syncDialog(dialogId) {
    const root = getLayerRoot();
    const wrapper = document.querySelector(
      `[data-tui-dialog][data-dialog-instance="${dialogId}"]`,
    );
    const backdrop = document.querySelector(
      `[data-tui-dialog-backdrop][data-dialog-instance="${dialogId}"]`,
    );
    const content = document.querySelector(
      `[data-tui-dialog-content][data-dialog-instance="${dialogId}"]`,
    );

    if (!wrapper) {
      backdrop?.remove();
      content?.remove();
      return;
    }

    if (backdrop && backdrop.parentElement !== root) {
      root.appendChild(backdrop);
    }
    if (content && content.parentElement !== root) {
      root.appendChild(content);
    }
  }

  function syncDialogs() {
    document.querySelectorAll("[data-tui-dialog]").forEach((wrapper) => {
      const dialogId = wrapper.getAttribute("data-dialog-instance");
      if (dialogId) syncDialog(dialogId);
    });
  }

  function updateBodyOverflow() {
    const hasOpenDialogs = document.querySelector(
      '[data-tui-dialog-content][data-tui-dialog-open="true"]',
    );
    document.body.style.overflow = hasOpenDialogs ? "hidden" : "";
  }

  function afterNextPaint(callback) {
    requestAnimationFrame(() => {
      requestAnimationFrame(callback);
    });
  }

  // Open dialog
  function openDialog(dialogId) {
    syncDialog(dialogId);

    // Find backdrop and content by instance ID
    const backdrop = document.querySelector(
      `[data-tui-dialog-backdrop][data-dialog-instance="${dialogId}"]`,
    );
    const content = document.querySelector(
      `[data-tui-dialog-content][data-dialog-instance="${dialogId}"]`,
    );

    if (!backdrop || !content) return;

    backdrop.setAttribute("data-tui-dialog-open", "false");
    content.setAttribute("data-tui-dialog-open", "false");
    backdrop.removeAttribute("data-tui-dialog-hidden");
    content.removeAttribute("data-tui-dialog-hidden");
    backdrop.offsetHeight;
    content.offsetHeight;

    // Then trigger the open animation after the browser painted the closed state
    afterNextPaint(() => {
      backdrop.setAttribute("data-tui-dialog-open", "true");
      content.setAttribute("data-tui-dialog-open", "true");
      updateBodyOverflow();

      // Update triggers
      document
        .querySelectorAll(
          `[data-tui-dialog-trigger][data-dialog-instance="${dialogId}"]`,
        )
        .forEach((trigger) => {
          trigger.setAttribute("data-tui-dialog-trigger-open", "true");
        });

      // Focus first focusable element (unless disabled)
      const disableAutoFocus = content.hasAttribute(
        "data-tui-dialog-disable-autofocus",
      );
      if (!disableAutoFocus) {
        setTimeout(() => {
          const focusable = content.querySelector(
            'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
          );
          focusable?.focus();
        }, 50);
      }

      // Focus trap
      content.addEventListener("keydown", trapFocus);
    });
  }

  // Trap focus within dialog content
  function trapFocus(e) {
    if (e.key !== "Tab") return;
    const content = e.currentTarget;
    const focusable = content.querySelectorAll(
      'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (e.shiftKey) {
      if (document.activeElement === first) {
        e.preventDefault();
        last.focus();
      }
    } else {
      if (document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }

  // Close dialog
  function closeDialog(dialogId) {
    // Return focus to the trigger that opened this dialog
    const trigger = document.querySelector(
      `[data-tui-dialog-trigger][data-dialog-instance="${dialogId}"][data-tui-dialog-trigger-open="true"]`,
    );

    // Find backdrop and content by instance ID
    const backdrop = document.querySelector(
      `[data-tui-dialog-backdrop][data-dialog-instance="${dialogId}"]`,
    );
    const content = document.querySelector(
      `[data-tui-dialog-content][data-dialog-instance="${dialogId}"]`,
    );

    if (!backdrop || !content) return;

    // Remove focus trap
    content.removeEventListener("keydown", trapFocus);

    // Start close animation
    backdrop.setAttribute("data-tui-dialog-open", "false");
    content.setAttribute("data-tui-dialog-open", "false");

    // Update triggers
    document
      .querySelectorAll(
        `[data-tui-dialog-trigger][data-dialog-instance="${dialogId}"]`,
      )
      .forEach((trigger) => {
        trigger.setAttribute("data-tui-dialog-trigger-open", "false");
      });

    // Wait for animation to complete before hiding
    setTimeout(() => {
      backdrop.setAttribute("data-tui-dialog-hidden", "true");
      content.setAttribute("data-tui-dialog-hidden", "true");
      updateBodyOverflow();

      // Return focus to trigger after animation
      if (trigger) {
        const btn = trigger.querySelector("button, a, [tabindex]") || trigger;
        btn.focus();
      }
    }, 300);
  }

  // Get dialog instance from element
  function getDialogInstance(element) {
    // Try to get from data attribute
    const instance = element.getAttribute("data-dialog-instance");
    if (instance) return instance;

    const owner = element.closest("[data-dialog-instance]");
    if (owner) return owner.getAttribute("data-dialog-instance");

    return null;
  }

  // Helper function for checking dialog state
  function isDialogOpen(dialogId) {
    const content = document.querySelector(
      `[data-tui-dialog-content][data-dialog-instance="${dialogId}"]`,
    );
    return content?.getAttribute("data-tui-dialog-open") === "true" || false;
  }

  // Helper function for toggling dialog
  function toggleDialog(dialogId) {
    isDialogOpen(dialogId) ? closeDialog(dialogId) : openDialog(dialogId);
  }

  // Event delegation
  document.addEventListener("click", (e) => {
    // Handle trigger clicks
    // Disabled buttons don't fire click events, so if we get here, it's enabled
    const trigger = e.target.closest("[data-tui-dialog-trigger]");
    if (trigger) {
      const dialogId = trigger.getAttribute("data-dialog-instance");
      if (!dialogId) return;

      toggleDialog(dialogId);
      return;
    }

    // Handle close button clicks
    const closeBtn = e.target.closest("[data-tui-dialog-close]");
    if (closeBtn) {
      // First check if the close button has a For value (dialog ID specified)
      const forValue = closeBtn.getAttribute("data-tui-dialog-close");
      const dialogId = forValue || getDialogInstance(closeBtn);

      if (dialogId) {
        closeDialog(dialogId);
      }
      return;
    }

    // Handle click away - close when clicking on backdrop
    const backdrop = e.target.closest("[data-tui-dialog-backdrop]");
    if (backdrop) {
      const dialogId = backdrop.getAttribute("data-dialog-instance");
      if (!dialogId) return;

      // Check if click away is disabled
      const wrapper = document.querySelector(
        `[data-tui-dialog][data-dialog-instance="${dialogId}"]`,
      );
      const content = document.querySelector(
        `[data-tui-dialog-content][data-dialog-instance="${dialogId}"]`,
      );

      const isDisabled =
        wrapper?.hasAttribute("data-tui-dialog-disable-click-away") ||
        content?.hasAttribute("data-tui-dialog-disable-click-away");

      if (!isDisabled) {
        closeDialog(dialogId);
      }
    }
  });

  // ESC key handler
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      // Find the most recently opened dialog (last in DOM)
      const openDialogs = document.querySelectorAll(
        '[data-tui-dialog-content][data-tui-dialog-open="true"]',
      );
      if (openDialogs.length === 0) return;

      const content = openDialogs[openDialogs.length - 1];
      const dialogId = content.getAttribute("data-dialog-instance");
      if (!dialogId) return;

      // Check if ESC is disabled
      const wrapper = document.querySelector(
        `[data-tui-dialog][data-dialog-instance="${dialogId}"]`,
      );

      const isDisabled =
        wrapper?.hasAttribute("data-tui-dialog-disable-esc") ||
        content?.hasAttribute("data-tui-dialog-disable-esc");

      if (!isDisabled) {
        closeDialog(dialogId);
      }
    }
  });

  // Initialize dialogs that should be open on load
  document.addEventListener("DOMContentLoaded", () => {
    syncDialogs();
    updateBodyOverflow();
  });

  // Cleanup when dialog elements are removed from DOM (HTMX, innerHTML, etc.)
  const observer = new MutationObserver(() => {
    syncDialogs();
    updateBodyOverflow();
  });

  // Start observing
  observer.observe(document.body, {
    childList: true,
    subtree: true,
  });

  // Expose public API
  window.tui = window.tui || {};
  window.tui.dialog = {
    open: openDialog,
    close: closeDialog,
    toggle: toggleDialog,
    isOpen: isDialogOpen,
  };
})();
