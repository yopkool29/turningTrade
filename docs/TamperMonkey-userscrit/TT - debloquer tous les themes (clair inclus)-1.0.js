// ==UserScript==
// @name         TT - debloquer tous les themes (clair inclus)
// @match        http://127.0.0.1:8765/*
// @match        http://localhost:8765/*
// @run-at       document-start
// @grant        none
// ==/UserScript==
//
// Le moteur HFQTheme connait ~29 themes mais le picker n'en "offre" que 7
// (THEME_OFFERTS). Ce script :
//  1. patche offert() -> tous les themes deviennent posables partout ;
//  2. ajoute dans le menu contextuel une entree repliable "Autres themes >"
//     juste apres les 7 themes -> sous-menu en place, groupe par famille
//     (Maison, Sombres, Clairs, Matieres, Degrades) ;
//  3. ajoute les memes groupes dans le dialog "Theme, barre du haut et
//     embleme..." (grille #apThemes) ;
//  4. complete THEME_GROUPES_OFFERTS -> le <select> du ladder affiche
//     tous les themes en <optgroup>.
//
// Pour forcer un theme au chargement : THEME_FORCE = "tt-clair".
// Themes clairs : "tt-clair", "pupitre", "parquet", "papier".

(function () {
  "use strict";

  var THEME_FORCE = null; // ex: "tt-clair"

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  function H() { return window.HFQTheme; }

  function actif() {
    var h = H();
    return h && h.actif ? h.actif() : h && h.DEFAUT;
  }

  // Themes non offerts, groupes par famille.
  function groupesCaches() {
    var h = H(), offerts = {};
    (h.THEME_OFFERTS || []).forEach(function (id) { offerts[id] = 1; });
    return (h.THEME_GROUPES || []).map(function (g) {
      return { nom: g.nom, ids: g.ids.filter(function (id) {
        return !offerts[id] && h.THEMES[id];
      }) };
    }).filter(function (g) { return g.ids.length; });
  }

  function poser(id) {
    try { if (window.poserPaletteGlobale) return void window.poserPaletteGlobale(id); } catch (e) {}
    try { H().poser(id); } catch (e) {}
  }

  // ---------- 1. Sous-menu repliable dans le menu contextuel ----------

  function itemTheme(id) {
    var k = H().THEMES[id];
    return '<div class="mi" data-tt-theme="' + esc(id) + '" title="' +
      esc(k.note || "") + '"><span>' + esc(k.nom) + '</span>' +
      '<span class="k">' + (actif() === id ? '<i class="hfq-check"></i>' : "") +
      "</span></div>";
  }

  function remplirBoite(box) {
    var html = "";
    groupesCaches().forEach(function (g) {
      html += '<div class="cap">' + esc(g.nom) + "</div>" +
        g.ids.map(itemTheme).join("");
    });
    box.innerHTML = html;
  }

  function augmenterMenu(menu) {
    // Le menu reconstruit ses items a chaque ouverture : on verifie la
    // presence reelle de nos noeuds, pas un flag sur l'element.
    if (menu.querySelector("[data-tt-extra]")) return;
    var h = H();
    if (!h || !groupesCaches().length) return;

    // Point d'ancrage : l'item "Couleur des fenetres" (input[type=color])
    // = juste apres les 7 vignettes du groupe Apparence.
    var anchor = null;
    var mis = menu.querySelectorAll(".mi");
    for (var i = 0; i < mis.length; i++) {
      if (mis[i].querySelector('input[type="color"]')) { anchor = mis[i]; break; }
    }
    if (!anchor) return;

    var btn = document.createElement("div");
    btn.className = "mi";
    btn.setAttribute("data-tt-extra", "1");
    btn.innerHTML = '<span>Autres thèmes</span><span class="k">▸</span>';

    var box = document.createElement("div");
    box.setAttribute("data-tt-extra", "1");
    box.style.display = "none";
    remplirBoite(box);

    var ouvert = false;
    btn.addEventListener("click", function (e) {
      e.preventDefault(); e.stopPropagation();
      ouvert = !ouvert;
      box.style.display = ouvert ? "" : "none";
      btn.querySelector(".k").textContent = ouvert ? "▾" : "▸";
      if (ouvert) remplirBoite(box); // rafraichit les coches
    });
    box.addEventListener("click", function (e) {
      var mi = e.target.closest("[data-tt-theme]");
      if (!mi) return;
      e.preventDefault(); e.stopPropagation();
      poser(mi.getAttribute("data-tt-theme"));
      remplirBoite(box);
    });
    // empeche le hover/le menu natif de refermer sur nos items
    box.addEventListener("mousedown", function (e) { e.stopPropagation(); });
    btn.addEventListener("mousedown", function (e) { e.stopPropagation(); });

    menu.insertBefore(btn, anchor);
    menu.insertBefore(box, anchor);
  }

  // ---------- 2. Groupes dans le dialog Apparence ----------------------

  function vignette(id) {
    var h = H(), k = h.THEMES[id];
    if (!k) return "";
    var bg = k.apercu ? k.apercu + " top/100% 8px no-repeat," + k.bg : k.bg;
    return '<button class="apth' + (actif() === id ? " on" : "") +
      '" data-theme="' + esc(id) + '" title="' + esc(k.note || "") + '">' +
      '<span class="ap-th-b" style="background:' + esc(bg) + '">' +
      '<i style="background:' + esc(k.bid) + '"></i>' +
      '<i style="background:' + esc(k.ask) + '"></i>' +
      '<i style="background:' + esc(k.poc) + '"></i></span>' +
      '<span class="nm">' + esc(k.nom) + "</span></button>";
  }

  function augmenterDialog() {
    var h = H();
    if (!h || !h.THEMES) return;
    var grid = document.querySelector("#hfqAppOv #apThemes");
    if (!grid || grid.querySelector("[data-tt-extra]")) return;
    var html = "";
    groupesCaches().forEach(function (g) {
      html += '<div class="aplab" data-tt-extra style="grid-column:1/-1;margin-top:10px">' +
        esc(g.nom) + "</div>" + g.ids.map(vignette).join("");
    });
    if (html) grid.insertAdjacentHTML("beforeend", html);
  }

  // ---------- 3. Passe-partout + select ladder -------------------------

  function debloquer() {
    var h = H();
    if (!h || !h.THEME_OFFERTS || !h.THEME_GROUPES) {
      return setTimeout(debloquer, 100);
    }
    try {
      h.offert = function () { return true; };

      var gOff = h.THEME_GROUPES_OFFERTS;
      if (gOff) {
        h.THEME_GROUPES.forEach(function (g) {
          var trouve = null;
          for (var k = 0; k < gOff.length; k++) {
            if (gOff[k].nom === g.nom) { trouve = gOff[k]; break; }
          }
          if (trouve) {
            g.ids.forEach(function (id) {
              if (trouve.ids.indexOf(id) < 0) trouve.ids.push(id);
            });
          } else {
            gOff.push(g);
          }
        });
      }
      if (window.HFQDom && HFQDom.THEME_GROUPES_OFFERTS !== gOff) {
        HFQDom.THEME_GROUPES_OFFERTS = gOff;
      }

      new MutationObserver(function () {
        augmenterDialog();
        var menu = document.getElementById("menupop");
        if (menu) augmenterMenu(menu);
      }).observe(document.documentElement, { childList: true, subtree: true });

      augmenterDialog();
      var menu = document.getElementById("menupop");
      if (menu) augmenterMenu(menu);

      if (THEME_FORCE && h.poser) {
        try { h.poser(THEME_FORCE); } catch (e) {}
      }
    } catch (e) {}
  }

  debloquer();
})();
