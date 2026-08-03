(() => {
  "use strict";

  const deck = document.querySelector("#deck");
  const slides = Array.from(document.querySelectorAll(".slide"));
  const previousButton = document.querySelector("#previous-slide");
  const nextButton = document.querySelector("#next-slide");
  const fullscreenButton = document.querySelector("#toggle-fullscreen");
  const counter = document.querySelector("#slide-counter");
  const progressBar = document.querySelector("#progress-bar");
  const themeSelect = document.querySelector("#theme-select");
  const themeStatus = document.querySelector("#theme-status");

  const themeStorageKey = "semantic-operator-deck-theme";
  const themes = themeSelect ? Array.from(themeSelect.options).map((option) => option.value) : ["operator"];

  const readStoredTheme = () => {
    try {
      return window.localStorage.getItem(themeStorageKey);
    } catch {
      return null;
    }
  };

  const applyTheme = (requestedTheme, options = {}) => {
    const theme = themes.includes(requestedTheme) ? requestedTheme : themes[0];
    document.documentElement.dataset.theme = theme;

    if (themeSelect) {
      themeSelect.value = theme;
    }

    if (options.persist !== false) {
      try {
        window.localStorage.setItem(themeStorageKey, theme);
      } catch {
        // Theme selection still works when storage is unavailable.
      }
    }

    if (options.announce && themeStatus && themeSelect) {
      const label = themeSelect.selectedOptions[0]?.textContent || theme;
      themeStatus.textContent = `${label} theme selected`;
    }
  };

  const cycleTheme = () => {
    const currentTheme = document.documentElement.dataset.theme;
    const currentThemeIndex = Math.max(0, themes.indexOf(currentTheme));
    applyTheme(themes[(currentThemeIndex + 1) % themes.length], { announce: true });
  };

  applyTheme(readStoredTheme() || themes[0], { persist: false });

  if (themeSelect) {
    themeSelect.addEventListener("change", () => applyTheme(themeSelect.value, { announce: true }));
  }

  if (!deck || slides.length === 0) return;

  let currentIndex = 0;

  const indexFromHash = () => {
    const index = slides.findIndex((slide) => `#${slide.id}` === window.location.hash);
    return index < 0 ? 0 : index;
  };

  const updateUi = (index) => {
    currentIndex = Math.max(0, Math.min(index, slides.length - 1));
    const slide = slides[currentIndex];

    counter.value = `${currentIndex + 1} / ${slides.length}`;
    counter.textContent = counter.value;
    progressBar.style.width = `${((currentIndex + 1) / slides.length) * 100}%`;
    previousButton.disabled = currentIndex === 0;
    nextButton.disabled = currentIndex === slides.length - 1;

    slides.forEach((item, itemIndex) => {
      item.setAttribute("aria-label", `Slide ${itemIndex + 1} of ${slides.length}: ${item.dataset.title || "Untitled"}`);
      item.setAttribute("aria-hidden", String(itemIndex !== currentIndex));
    });

    document.title = `${slide.dataset.title || "Slide"} · semantic-operator`;
  };

  const setHash = (slide, mode) => {
    const hash = `#${slide.id}`;
    if (window.location.hash === hash) return;

    if (mode === "push") {
      window.history.pushState(null, "", hash);
    } else {
      window.history.replaceState(null, "", hash);
    }
  };

  const goToSlide = (index, options = {}) => {
    const nextIndex = Math.max(0, Math.min(index, slides.length - 1));
    const slide = slides[nextIndex];
    const behavior = window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";

    slide.scrollIntoView({ behavior, block: "start" });
    updateUi(nextIndex);
    setHash(slide, options.history || "push");

    if (options.focus) {
      slide.focus({ preventScroll: true });
    }
  };

  const toggleFullscreen = async () => {
    try {
      if (document.fullscreenElement) {
        await document.exitFullscreen();
      } else {
        await document.documentElement.requestFullscreen();
      }
    } catch {
      // Fullscreen may be blocked by the browser or embedding context.
    }
  };

  previousButton.addEventListener("click", () => goToSlide(currentIndex - 1, { focus: true }));
  nextButton.addEventListener("click", () => goToSlide(currentIndex + 1, { focus: true }));

  if (document.fullscreenEnabled) {
    fullscreenButton.addEventListener("click", toggleFullscreen);
    document.addEventListener("fullscreenchange", () => {
      const isFullscreen = Boolean(document.fullscreenElement);
      fullscreenButton.setAttribute("aria-label", isFullscreen ? "Exit fullscreen" : "Enter fullscreen");
    });
  } else {
    fullscreenButton.hidden = true;
  }

  document.addEventListener("keydown", (event) => {
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    if (event.target instanceof HTMLElement && event.target.closest("button, a, input, textarea, select, pre")) return;

    const nextKeys = new Set(["ArrowRight", "ArrowDown", "PageDown", " "]);
    const previousKeys = new Set(["ArrowLeft", "ArrowUp", "PageUp"]);

    if (nextKeys.has(event.key)) {
      event.preventDefault();
      goToSlide(currentIndex + 1, { focus: true });
    } else if (previousKeys.has(event.key)) {
      event.preventDefault();
      goToSlide(currentIndex - 1, { focus: true });
    } else if (event.key === "Home") {
      event.preventDefault();
      goToSlide(0, { focus: true });
    } else if (event.key === "End") {
      event.preventDefault();
      goToSlide(slides.length - 1, { focus: true });
    } else if (event.key.toLowerCase() === "f" && document.fullscreenEnabled) {
      event.preventDefault();
      toggleFullscreen();
    } else if (event.key.toLowerCase() === "t") {
      event.preventDefault();
      cycleTheme();
    }
  });

  window.addEventListener("popstate", () => goToSlide(indexFromHash(), { history: "replace" }));

  const observer = new IntersectionObserver(
    (entries) => {
      const visible = entries
        .filter((entry) => entry.isIntersecting)
        .sort((a, b) => b.intersectionRatio - a.intersectionRatio)[0];

      if (!visible) return;
      const index = slides.indexOf(visible.target);
      if (index !== currentIndex) {
        updateUi(index);
        setHash(slides[index], "replace");
      }
    },
    { root: deck, threshold: [0.55, 0.75, 0.95] }
  );

  slides.forEach((slide) => observer.observe(slide));

  currentIndex = indexFromHash();
  updateUi(currentIndex);
  setHash(slides[currentIndex], "replace");
  window.requestAnimationFrame(() => {
    slides[currentIndex].scrollIntoView({ behavior: "auto", block: "start" });
  });
})();
