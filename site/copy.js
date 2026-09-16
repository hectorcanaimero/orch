// A "Copy" button next to a command: [data-copy] copies the text of the
// [data-copy-source] inside the same parent. Buttons stay hidden without the
// Clipboard API, so nothing on the page promises what the browser cannot do.
(function () {
  "use strict";
  if (!navigator.clipboard) return;
  document.querySelectorAll("[data-copy]").forEach(function (button) {
    var source = button.parentElement.querySelector("[data-copy-source]");
    if (!source) return;
    button.hidden = false;
    button.addEventListener("click", function () {
      navigator.clipboard.writeText(source.textContent.trim()).then(function () {
        button.textContent = "Copied";
        setTimeout(function () { button.textContent = "Copy"; }, 1600);
      }, function () {
        button.textContent = "Select and copy";
      });
    });
  });
})();
